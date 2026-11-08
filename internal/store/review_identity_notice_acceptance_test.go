package store_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func reviewIdentityNoticeText(t *testing.T, reviewID string) string {
	t.Helper()
	f, _ := importedOutcomeNotice(t)
	outcome := messaging.ReviewOutcome{AskID: "ask", Reviewer: "target", Seq: 2, State: "declined", Reason: "unavailable", ReviewID: reviewID}
	answer, err := f.db.PeerMessages().AcceptOutcome(context.Background(), f.key, outcome, messageAt)
	pairOK(t, err)
	pairEqual(t, answer.Stored, true)
	imports, err := f.db.PendingImports(context.Background(), 10)
	pairOK(t, err)
	for _, imp := range imports {
		if imp.Kind != "outcome" {
			continue
		}
		result, err := f.db.ImportRemote(context.Background(), imp, 3, messageAt)
		pairOK(t, err)
		var payload struct {
			Text string `json:"text"`
		}
		pairOK(t, json.Unmarshal(result.Msg.Payload, &payload))
		return payload.Text
	}
	t.Fatal("no review outcome notice imported")
	return ""
}

func TestReviewIdentityNoticeAcceptanceNamesBothIDs(t *testing.T) {
	id := `reviewer/foreign"259`
	text := reviewIdentityNoticeText(t, id)
	for _, want := range []string{strconv.Quote("ask"), "their review ID", strconv.Quote(id)} {
		if !strings.Contains(text, want) {
			t.Fatalf("outcome text missing %q: %s", want, text)
		}
	}
}

func TestReviewIdentityNoticeAcceptanceUnknownIDIsExplicit(t *testing.T) {
	text := reviewIdentityNoticeText(t, "")
	pairEqual(t, strings.Contains(text, strconv.Quote("ask")), true)
	pairEqual(t, strings.Contains(text, "reviewer ID not reported"), true)
	pairEqual(t, strings.Contains(text, "older pairtask"), false)
}
