//go:build linux

package server

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func staleOperatorReplacement(f *outcomeNoticeServer, id, parent string) map[string]any {
	request := staleReplacement(f, id, parent)
	return map[string]any{"contact_op": protocol.ContactOpReviewAsk, "contact_key": f.key, "contact_to": []string{"target"}, "message_id": id, "review_ask": request["review_ask"]}
}

func TestStaleAskOperatorAcceptanceAgentAndOperatorCannotReplaceEachOthersAsks(t *testing.T) {
	f := staleFinalAsk(t)
	owner := sessionOpen(f.operatorFixture)
	response, _ := owner.request(protocol.TypeOperatorContact, staleOperatorReplacement(f, "operator-replaces-agent", "ask"))
	operatorEqual(t, "operator cannot take agent parent", response.text("code"), "not_supersedable")
	initial := staleOperatorReplacement(f, "operator-parent", "")
	response, _ = owner.request(protocol.TypeOperatorContact, initial)
	operatorEqual(t, "operator can originate own ask", response.text("type"), protocol.TypeOperatorContactResult)
	result, err := f.st.PeerMessages().AcceptOutcome(context.Background(), f.key, messaging.ReviewOutcome{AskID: "operator-parent", Reviewer: "target", Seq: 1, State: "declined", Reason: "unavailable"}, time.Now())
	operatorNoError(t, err)
	operatorEqual(t, "operator ask finalized", result.Stored, true)
	operatorEqual(t, "agent cannot take operator parent", staleRawRequest(t, f.agent, staleReplacement(f, "agent-replaces-operator", "operator-parent")).Code, "not_supersedable")
	response, _ = owner.request(protocol.TypeOperatorContact, staleOperatorReplacement(f, "operator-child", "operator-parent"))
	operatorEqual(t, "operator can replace own final ask", response.text("type"), protocol.TypeOperatorContactResult)
	parent, found, err := f.st.PeerMessages().AskOutFor(context.Background(), f.key, "operator-parent")
	operatorNoError(t, err)
	operatorEqual(t, "parent retained", found, true)
	operatorEqual(t, "operator committed replacement link", staleJSON(t, parent.View())["superseded_by"], any("operator-child"))
}
