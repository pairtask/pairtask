package hub

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestClassifierReportAcceptanceClaimsAreWeakLabelsAndReadsDoNotMutate(t *testing.T) {
	f := newClassifierFixture(t, nil)
	at := time.Now().UTC().Add(-time.Minute)
	for i, id := range []string{"done-agree", "active-disagree", "unclaimed", "released"} {
		f.send(t, protocol.Message{MessageID: id})
		for _, c := range f.clients {
			recv(t, c)
		}
		p := store.Prediction{MessageID: id, Outcome: store.PredictionPredicted, AgentID: "tester", Provider: "typesafe", Model: "jev-latest", ModelVersion: "jev-1.14.0", Threshold: .7, LatencyMS: int64(10 + i*10), CreatedAt: at.Add(time.Duration(i) * time.Second)}
		if _, err := f.h.store.RecordPrediction(f.ctx, p); err != nil {
			t.Fatal(err)
		}
		if id == "unclaimed" {
			continue
		}
		claim := store.ClaimRequest{MessageID: id, Agent: "tester", Task: protocol.TaskReply, Role: protocol.RoleOwner, At: at}
		if id == "active-disagree" {
			claim.Agent = "builder"
		}
		if _, err := f.h.store.SetClaim(f.ctx, claim); err != nil {
			t.Fatal(err)
		}
		if id == "done-agree" {
			if _, err := f.h.store.FinishClaim(f.ctx, claim); err != nil {
				t.Fatal(err)
			}
		}
		if id == "released" {
			if _, err := f.h.store.ReleaseClaim(f.ctx, claim); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := f.h.store.RecordPrediction(f.ctx, store.Prediction{MessageID: "skip", Outcome: store.PredictionSkipped, Reason: "tagged", LatencyMS: 999999, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.store.RecordPrediction(f.ctx, store.Prediction{MessageID: "before-cutoff", Outcome: store.PredictionError, CreatedAt: at.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	before, _, err := f.h.store.PredictionsSince(f.ctx, at, 100)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	out := f.h.SubmitOperator(ctx, 0, protocol.Message{Type: protocol.TypeOperatorClassifierReport, ClassifierRequest: &protocol.ClassifierReportRequest{Since: at}})
	if out.Type != protocol.TypeOperatorClassifierReport || out.ClassifierReport == nil {
		t.Fatalf("report request failed: %s %s", out.Type, out.Detail)
	}
	r := out.ClassifierReport
	if r.Count != 5 || r.Outcomes[store.PredictionPredicted] != 4 || r.Outcomes[store.PredictionSkipped] != 1 || r.LatencyMS.Asked != 4 || r.LatencyMS.Max != 40 {
		t.Errorf("summary/cutoff/latency incorrect: %+v", r)
	}
	if r.Agreement.Compared != 2 || r.Agreement.Agreed != 1 || r.Agreement.Disagreed != 1 || r.Agreement.Unclaimed != 2 {
		t.Errorf("done/active/released claim labels incorrect: %+v", r.Agreement)
	}
	after, _, err := f.h.store.PredictionsSince(f.ctx, at, 100)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("report mutated predictions")
	}
}

func TestClassifierReportAcceptanceTruncationKeepsNewestWindow(t *testing.T) {
	f := newClassifierFixture(t, nil)
	at := time.Now().UTC().Add(-time.Hour)
	for i := 0; i <= protocol.ClassifierReportMax; i++ {
		p := store.Prediction{MessageID: fmt.Sprintf("item-%04d", i), Outcome: store.PredictionSkipped, Reason: "tagged", CreatedAt: at.Add(time.Duration(i) * time.Second)}
		if _, err := f.h.store.RecordPrediction(f.ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	out := f.h.SubmitOperator(ctx, 0, protocol.Message{Type: protocol.TypeOperatorClassifierReport, ClassifierRequest: &protocol.ClassifierReportRequest{Since: at}})
	r := out.ClassifierReport
	if r == nil {
		t.Fatalf("missing report: %s %s", out.Type, out.Detail)
	}
	if !r.Truncated || r.Count != protocol.ClassifierReportMax || len(r.Predictions) != protocol.ClassifierReportListed {
		t.Fatalf("report bound: %+v", r)
	}
	if r.Predictions[0].MessageID != fmt.Sprintf("item-%04d", protocol.ClassifierReportMax) {
		t.Fatalf("newest prediction absent: first=%s", r.Predictions[0].MessageID)
	}
}
