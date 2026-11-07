//go:build linux

package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func stalePins() protocol.Revisions {
	return protocol.Revisions{Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}
}

func staleMoved() protocol.Revisions {
	return protocol.Revisions{Base: strings.Repeat("1", 40), Head: strings.Repeat("3", 40)}
}

func staleReviewServer(t *testing.T, version int) *noticeGuidanceServer {
	t.Helper()
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	key := id.Fingerprint()
	agent := f.peer("reviewer", version)
	grant := f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewGrant, ContactKey: key,
		ContactAgents: []string{"reviewer"}, ContactReviewGrant: &protocol.ContactReviewGrant{Mode: "auto"}, MessageID: "stale-grant"})
	operatorEqual(t, "grant accepted", grant.Type, protocol.TypeOperatorContactResult)
	pins := stalePins()
	m := messaging.Message{ID: "guidance-ask", N: 1, Text: "review pinned change", From: "requester", To: []string{"reviewer"}, CreatedAt: time.Now(),
		Review: &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "259", Base: pins.Base, Head: pins.Head}}
	_, err := f.st.PeerMessages().Accept(context.Background(), key, m, time.Now())
	operatorNoError(t, err)
	shared := f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpShare, ContactKey: key,
		ContactAgents: []string{"reviewer"}, MessageID: "stale-share"})
	operatorEqual(t, "reviewer shared", shared.Type, protocol.TypeOperatorContactResult)
	notice := agent.read()
	operatorEqual(t, "review notice delivered", notice.ReviewID != "", true)
	acknowledgeRemoteAgent(t, agent, notice.MessageID)
	return &noticeGuidanceServer{f, key, agent, notice}
}

func staleCheck(f *noticeGuidanceServer, revisions protocol.Revisions) protocol.Message {
	f.agent.write(protocol.Message{Type: "review_check_pins", RequestID: "pin-check", ReviewID: f.notice.ReviewID, Revisions: &revisions})
	return f.agent.read()
}

func staleJSON(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	operatorNoError(t, err)
	var fields map[string]any
	operatorNoError(t, json.Unmarshal(data, &fields))
	return fields
}

func staleAssertMoved(t *testing.T, value any, want protocol.Revisions) {
	t.Helper()
	fields := staleJSON(t, value)
	moved, ok := fields["moved_to"].(map[string]any)
	if !ok {
		t.Fatalf("missing structured moved_to: %s", sessionJSON(value))
	}
	operatorEqual(t, "observed base", moved["base"], any(want.Base))
	operatorEqual(t, "observed head", moved["head"], any(want.Head))
}
