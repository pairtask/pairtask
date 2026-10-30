package messaging_test

import (
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestOutcomeWireAcceptanceRoundTripAndDigest(t *testing.T) {
	o := messaging.ReviewOutcome{AskID: "ask", Reviewer: "reviewer/@\nname", Seq: 3, Attempt: 1, Intent: 1, State: "published", Verdict: "comment", Summary: "reviewed", URL: "https://example.test/review", Reason: "done"}
	wire, err := messaging.EncodeOutcome(o)
	sessionOK(t, err)
	got, err := messaging.DecodeOutcome(wire)
	sessionOK(t, err)
	if !reflect.DeepEqual(got, o) {
		t.Fatalf("outcome changed: %+v", got)
	}
	for field, mutate := range map[string]func(*messaging.ReviewOutcome){
		"ask":      func(x *messaging.ReviewOutcome) { x.AskID = "other" },
		"reviewer": func(x *messaging.ReviewOutcome) { x.Reviewer = "other" },
		"seq":      func(x *messaging.ReviewOutcome) { x.Seq++ },
		"attempt":  func(x *messaging.ReviewOutcome) { x.Attempt++ },
		"intent":   func(x *messaging.ReviewOutcome) { x.Intent++ },
		"state":    func(x *messaging.ReviewOutcome) { x.State = "uncertain" },
		"verdict":  func(x *messaging.ReviewOutcome) { x.Verdict = "approve" },
		"summary":  func(x *messaging.ReviewOutcome) { x.Summary = "other" },
		"url":      func(x *messaging.ReviewOutcome) { x.URL = "other" },
		"reason":   func(x *messaging.ReviewOutcome) { x.Reason = "other" },
	} {
		changed := o
		mutate(&changed)
		if changed.Digest() == o.Digest() {
			t.Fatalf("%s omitted from digest", field)
		}
	}
}
