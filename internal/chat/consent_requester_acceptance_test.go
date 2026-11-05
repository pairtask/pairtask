package chat_test

import (
	"encoding/json"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
	"strings"
	"testing"
)

func consentRequesterRow(t *testing.T, state, reason string, review bool) protocol.ContactMessageView {
	t.Helper()
	row := contactLayoutRow("correlated-consent-request", 1)
	row.Direction = "out"
	row.State = "delivered"
	row.Text = "ONE CORRELATED REQUEST BODY"
	row.To = []string{"reviewer"}
	if review {
		row.ReviewAsk = &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "293"}, Base: "base", Head: "head"}
		row.ReviewOutcome = &protocol.ReviewOutcomeView{AskID: row.ID, Reviewer: "reviewer", State: state}
	}
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err = json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	object["consent"], object["consent_reason"] = state, reason
	data, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestConsentRequesterAcceptanceHeldStatusRidesOnOneOutgoingRow(t *testing.T) {
	h := contactLayoutHarness(t, consentRequesterRow(t, "held", "", false))
	frame := ansi.Strip(h.m.View().Content)
	if strings.Count(frame, "ONE CORRELATED REQUEST BODY") != 1 {
		t.Fatal("held request duplicated or lost its original row")
	}
	if strings.Count(frame, "held for their operator") != 1 {
		t.Fatalf("one outgoing request lacks its held status: %s", frame)
	}
}

func TestConsentRequesterAcceptanceReviewAndConsentSessionEndCoalesce(t *testing.T) {
	h := contactLayoutHarness(t, consentRequesterRow(t, "withheld", "session_ended", true))
	frame := strings.ToLower(ansi.Strip(h.m.View().Content))
	if strings.Count(frame, "one correlated request body") != 1 {
		t.Fatal("review and consent produced duplicate request rows")
	}
	if strings.Count(frame, "session ended") != 1 || !strings.Contains(frame, "not run:") {
		t.Fatalf("request lacks one actionable session-ended summary: %s", frame)
	}
	if strings.Contains(frame, "held for their operator") {
		t.Fatal("settled request still presented as pending consent")
	}
}

func TestConsentRequesterAcceptanceAskedItemUsesOneCorrelatedSummary(t *testing.T) {
	h := reviewCountsHarness(t, &protocol.ReviewCounts{})
	v2Command(h, "/reviews")
	for range 3 {
		req := h.take(protocol.TypeOperatorReview)
		if req.ReviewOp != protocol.ReviewOpAsked {
			reviewPendingResponse(h, req, protocol.ReviewAwaitingPage{})
			continue
		}
		data, err := json.Marshal(map[string]any{"type": "operator_review_result", "review_asked": map[string]any{"total": 1, "items": []any{map[string]any{"contact": "canonical-peer", "ask_id": "one-correlated-ask", "reviewer": "reviewer", "state": "withheld", "reason": "session_ended", "settled": true, "consent": "withheld", "consent_reason": "session_ended"}}}})
		if err != nil {
			t.Fatal(err)
		}
		var response protocol.Message
		if err = json.Unmarshal(data, &response); err != nil {
			t.Fatal(err)
		}
		h.reply(req, response)
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
	frame := strings.ToLower(ansi.Strip(h.m.View().Content))
	if strings.Count(frame, "session ended") != 1 || !strings.Contains(frame, "not run:") {
		t.Fatalf("Asked item did not coalesce review/consent into one ended request: %s", frame)
	}
}

func TestConsentRequesterAcceptanceInitialDeclineExpiryHaveOneTerminalSummary(t *testing.T) {
	for _, state := range []string{"declined", "expired"} {
		t.Run(state, func(t *testing.T) {
			h := contactLayoutHarness(t, consentRequesterRow(t, state, map[string]string{"declined": "denied", "expired": "expired"}[state], true))
			frame := strings.ToLower(ansi.Strip(h.m.View().Content))
			if strings.Count(frame, "one correlated request body") != 1 || strings.Count(frame, "not run:") != 1 {
				t.Fatalf("terminal consent did not coalesce one request summary: %s", frame)
			}
			if !strings.Contains(frame, state) || strings.Contains(frame, "held for their operator") {
				t.Fatalf("terminal consent summary is stale or wrong: %s", frame)
			}
		})
	}
}
