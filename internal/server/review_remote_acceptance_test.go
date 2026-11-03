//go:build linux

package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReviewRemoteServerAcceptancePinnedOperatorRequestHonoursRemoteGrant(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, allowed := range []bool{false, true} {
			t.Run(fmt.Sprintf("reverse=%v/allowed=%v", reverse, allowed), func(t *testing.T) {
				p := newInstantPair(t)
				sides := p.sides()
				if reverse {
					sides[0], sides[1] = sides[1], sides[0]
				}
				remoteServerRequest(t, sides, allowed)
				p.assertOneConnection(t)
			})
		}
	}
}

func remoteServerRequest(t *testing.T, sides []instantSide, allowed bool) {
	t.Helper()
	sender, receiver := sides[0], sides[1]
	reviewer := receiver.f.peer("reviewer", protocol.Version)
	if allowed {
		grant := receiver.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewGrant, ContactKey: receiver.key, ContactAgents: []string{"reviewer"}, ContactReviewGrant: &protocol.ContactReviewGrant{Mode: "auto", Publish: true, Formal: true}, MessageID: "grant-reviews"})
		operatorEqual(t, "receiver review grant", grant.Type, protocol.TypeOperatorContactResult)
	}
	pins := protocol.Revisions{Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}
	ask := &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "295"}, Base: pins.Base, Head: pins.Head, Scope: "inspect retries", Publish: false, Formal: false}
	queued := sender.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewAsk, ContactKey: sender.key, ContactTo: []string{"reviewer"}, MessageID: "operator-ui-review", ReviewAsk: ask})
	operatorEqual(t, "operator command queued", queued.Type, protocol.TypeOperatorContactResult)
	if !allowed {
		refused := sender.awaitRemoteView(t, func(row protocol.ContactMessageView) bool {
			return row.ID == "operator-ui-review" && row.State == "refused"
		})
		operatorEqual(t, "remote grant remains authoritative", refused.RefusalReason, "not_shared")
		operatorEqual(t, "no review imported without grant", receiver.f.count("reviews"), 0)
		return
	}
	notice := reviewer.read()
	if notice.ReviewID == "" {
		t.Fatal("remote operator request has no reviewer notice")
	}
	rec, found, err := receiver.f.st.GetReview(context.Background(), notice.ReviewID)
	operatorNoError(t, err)
	operatorEqual(t, "review imported", found, true)
	if rec.View.Pinned == nil || rec.View.Spec.Policy == nil {
		t.Fatal("remote review lost pins or publication policy")
	}
	operatorEqual(t, "requester's exact pins", *rec.View.Pinned, pins)
	operatorEqual(t, "receiver grant never broadens publish", rec.View.Spec.Policy.Publish, false)
	operatorEqual(t, "receiver grant never broadens formal", rec.View.Spec.Policy.Formal, false)
	operatorEqual(t, "requester scope", rec.View.Spec.Scope, "inspect retries")
	operatorEqual(t, "operator origin retained", notice.Origin, protocol.OriginRemote)
}
