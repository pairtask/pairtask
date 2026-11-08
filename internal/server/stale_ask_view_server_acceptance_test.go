//go:build linux

package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func staleVersionedOwner(t *testing.T, f *outcomeNoticeServer, version int) *sessionProbe {
	t.Helper()
	p := &sessionProbe{operatorPeer: operatorDial(t, f.operator)}
	p.send(map[string]any{"type": protocol.TypeOperatorSessionOpen, "request_id": "stale-open", "protocol_version": version})
	operatorEqual(t, "operator session opened", p.frame().text("type"), protocol.TypeOperatorSession)
	p.snapshot()
	return p
}

func staleOperatorAskedItem(t *testing.T, response sessionWire, id string) map[string]any {
	t.Helper()
	var page struct {
		Items []map[string]any `json:"items"`
	}
	operatorNoError(t, json.Unmarshal(response["review_asked"], &page))
	for _, item := range page.Items {
		if item["ask_id"] == id {
			return item
		}
	}
	t.Fatalf("operator asked response missing %s", id)
	return nil
}

func TestStaleAskViewsAcceptanceOperatorAskedListAndDetail(t *testing.T) {
	for _, op := range []string{protocol.ReviewOpAsked, protocol.ReviewOpAskedDetail} {
		t.Run(op, func(t *testing.T) {
			f := newOutcomeNoticeServer(t)
			reason := "moved base=" + staleMoved().Base + " head=" + staleMoved().Head
			notice := findingsServerNotice(t, f, messaging.ReviewOutcome{AskID: "ask", Reviewer: "target", Seq: 2, State: "declined", Reason: reason})
			acknowledgeRemoteAgent(t, f.agent, notice.MessageID)
			operatorEqual(t, "replacement queued", staleRawRequest(t, f.agent, staleReplacement(f, "replacement", "ask")).Stage, protocol.StageAccepted)
			owner := staleVersionedOwner(t, f, 26)
			fields := map[string]any{"review_op": op}
			if op == protocol.ReviewOpAskedDetail {
				fields["review_ask_refs"] = []protocol.ReviewAskRef{{Contact: f.key, AskID: "ask"}}
			}
			response, _ := owner.request(protocol.TypeOperatorReview, fields)
			operatorEqual(t, "operator asked succeeds", response.text("type"), protocol.TypeOperatorReviewResult)
			parent := staleOperatorAskedItem(t, response, "ask")
			operatorEqual(t, "operator parent links replacement", parent["superseded_by"], any("replacement"))
			staleAssertMoved(t, parent, staleMoved())
			if op == protocol.ReviewOpAskedDetail {
				fields["review_ask_refs"] = []protocol.ReviewAskRef{{Contact: f.key, AskID: "replacement"}}
				response, _ = owner.request(protocol.TypeOperatorReview, fields)
				operatorEqual(t, "child detail succeeds", response.text("type"), protocol.TypeOperatorReviewResult)
			}
			child := staleOperatorAskedItem(t, response, "replacement")
			operatorEqual(t, "operator child links parent", child["supersedes"], any("ask"))
		})
	}
}

func TestStaleAskViewsAcceptanceLegacyReviewerOmitsMovedTo(t *testing.T) {
	for _, id := range []string{"single", "page"} {
		t.Run(id, func(t *testing.T) {
			f := staleReviewServer(t, 25)
			moved := staleMoved()
			f.agent.write(protocol.Message{Type: protocol.TypeReviewPrepare, RequestID: "legacy-move", ReviewID: f.notice.ReviewID, Revisions: &moved})
			operatorEqual(t, "legacy decline committed", f.agent.read().Code, protocol.ErrReviewConflict)
			request := protocol.Message{Type: protocol.TypeReviewGet, RequestID: "legacy-read"}
			if id == "single" {
				request.ReviewID = f.notice.ReviewID
			}
			f.agent.write(request)
			response := f.agent.read()
			operatorEqual(t, "legacy reads review", response.Type, protocol.TypeReviewResult)
			data, err := json.Marshal(response)
			operatorNoError(t, err)
			operatorEqual(t, "v26 metadata omitted from legacy review view", strings.Contains(string(data), `"moved_to"`), false)
		})
	}
}
