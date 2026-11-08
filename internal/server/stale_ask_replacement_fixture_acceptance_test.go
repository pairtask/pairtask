//go:build linux

package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func staleFinalAsk(t *testing.T) *outcomeNoticeServer {
	t.Helper()
	f := newOutcomeNoticeServer(t)
	notice := findingsServerNotice(t, f, messaging.ReviewOutcome{AskID: "ask", Reviewer: "target", Seq: 2, State: "declined", Reason: "reviewer unavailable"})
	acknowledgeRemoteAgent(t, f.agent, notice.MessageID)
	return f
}

func staleReplacement(f *outcomeNoticeServer, id, parent string) map[string]any {
	pins := staleMoved()
	return map[string]any{"type": protocol.TypeMsg, "message_id": id, "workspace": f.key, "to": "target", "durable": true, "ack_requested": true,
		"payload": map[string]any{"text": "explicit replacement"}, "review_ask": map[string]any{"forge": "github", "repository": "pairtask/pairtask", "change": "259", "base": pins.Base, "head": pins.Head, "supersedes": parent}}
}

func staleRawRequest(t *testing.T, p *operatorPeer, fields map[string]any) protocol.Message {
	t.Helper()
	operatorNoError(t, p.SetWriteDeadline(time.Now().Add(2*time.Second)))
	operatorNoError(t, json.NewEncoder(p).Encode(fields))
	return p.read()
}

func staleAsked(t *testing.T, f *outcomeNoticeServer, id string) map[string]any {
	t.Helper()
	f.agent.write(protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "replacement-detail", ReviewAskRefs: []protocol.ReviewAskRef{{Contact: f.key, AskID: id}}})
	response := f.agent.read()
	operatorEqual(t, "asked detail supported", response.Type, protocol.TypeReviewsAskedResult)
	operatorEqual(t, "exactly one requested detail", len(response.ReviewsAsked), 1)
	return staleJSON(t, response.ReviewsAsked[0])
}

func staleReplacementAsk(fields map[string]any) map[string]any {
	return fields["review_ask"].(map[string]any)
}
