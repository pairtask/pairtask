//go:build linux

package server

import (
	"fmt"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestRemoteReviewE2EApprovalBothDirections(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			p := newInstantPair(t)
			sides := p.sides()
			if reverse {
				sides[0], sides[1] = sides[1], sides[0]
			}
			remoteReviewApproval(t, sides, false)
			p.assertOneConnection(t)
		})
	}
}

func remoteReviewApproval(t *testing.T, sides []instantSide, operator bool) {
	t.Helper()
	sender, receiver := sides[0], sides[1]
	const asker = "asking/@\nagent"
	const reviewer = "reviewer/@\nagent"
	a, b := sender.f.peer(asker, 14), receiver.f.peer(reviewer, 14)
	sender.shareRemoteAgents(t, asker)
	grant := receiver.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewGrant, ContactKey: receiver.key, ContactAgents: []string{reviewer}, ContactReviewGrant: &protocol.ContactReviewGrant{Mode: "ask"}, MessageID: "review-grant"})
	operatorEqual(t, "review grant", grant.Type, protocol.TypeOperatorContactResult)
	ask := &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "235"}, Base: "base", Head: "head", Publish: true, Formal: true}
	if operator {
		result := sender.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewAsk, ContactKey: sender.key, ContactTo: []string{reviewer}, MessageID: "remote-review", ReviewAsk: ask})
		operatorEqual(t, "operator ask queued", result.Type, protocol.TypeOperatorContactResult)
	} else {
		a.write(protocol.Message{Type: protocol.TypeMsg, Workspace: sender.key, To: reviewer, MessageID: "remote-review", ReviewAsk: ask, Durable: true, AckRequested: true})
		operatorEqual(t, "ask queued", a.read().Stage, protocol.StageAccepted)
	}
	pending := receiver.awaitRemoteView(t, func(m protocol.ContactMessageView) bool {
		return m.ID == "remote-review" && m.ReviewState == "awaiting"
	})
	operatorEqual(t, "not dispatched", pending.ReviewID, "")
	if pending.ReviewAsk == nil {
		t.Fatal("operator cannot inspect pending ask")
	}
	operatorEqual(t, "frozen head", pending.ReviewAsk.Head, "head")
	approve := true
	result := receiver.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewDecide, ContactKey: receiver.key, ContactRead: []string{"remote-review"}, ContactApprove: &approve, MessageID: "approve-review"})
	operatorEqual(t, "operator approval", result.Type, protocol.TypeOperatorContactResult)
	got := b.read()
	assertReviewNoticeOrigin(t, got, receiver.key, operator)
	if got.ReviewID == "" {
		t.Fatal("reviewer notification lacks review ID")
	}
	imported := receiver.awaitRemoteView(t, func(m protocol.ContactMessageView) bool {
		return m.ID == "remote-review" && m.ReviewState == "imported"
	})
	operatorEqual(t, "notice matches operator review", got.ReviewID, imported.ReviewID)
	acknowledgeRemoteAgent(t, b, got.MessageID)
}

func TestRemoteReviewE2EOperatorApprovalBothDirections(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			p := newInstantPair(t)
			sides := p.sides()
			if reverse {
				sides[0], sides[1] = sides[1], sides[0]
			}
			remoteReviewApproval(t, sides, true)
			p.assertOneConnection(t)
		})
	}
}

func assertReviewNoticeOrigin(t *testing.T, got protocol.Message, contact string, operator bool) {
	t.Helper()
	if operator {
		operatorEqual(t, "remote operator origin", got.Origin, protocol.OriginRemote)
		operatorEqual(t, "remote operator sender", got.From, protocol.OperatorID)
		if got.Remote == nil {
			t.Fatal("missing operator remote identity")
		}
		operatorEqual(t, "remote operator contact", got.Remote.Contact, contact)
		operatorEqual(t, "operator metadata sender", got.Remote.Sender, "")
	} else {
		requireWorkspaceAgent(t, got, contact, "asking/@\nagent")
	}
}
