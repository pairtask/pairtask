package hub

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestClassifierReportAcceptanceClaimsAreWeakLabelsAndReadsDoNotMutate(t *testing.T) {
	f := newClassifierFixture(t, nil)
	at := time.Now().UTC().Add(-time.Minute)
	for i, id := range []string{"done-agree", "active-disagree", "unclaimed", "released"} {
		seedClaimedPrediction(t, f, reportSeed{id: id, at: at.Add(time.Duration(i) * time.Second), latency: int64(10 + i*10)})
	}
	f.seedPrediction(t, store.Prediction{MessageID: "skip", Outcome: store.PredictionSkipped, Reason: "tagged", LatencyMS: 999999, CreatedAt: at})
	f.seedPrediction(t, store.Prediction{MessageID: "before-cutoff", Outcome: store.PredictionError, CreatedAt: at.Add(-time.Hour)})
	before := f.predictionRows(t, at)
	r := f.report(t, at)
	classifierEqual(t, r.Count, 5)
	classifierEqual(t, r.Outcomes[store.PredictionPredicted], 4)
	classifierEqual(t, r.Outcomes[store.PredictionSkipped], 1)
	classifierEqual(t, r.LatencyMS.Asked, 4)
	classifierEqual(t, r.LatencyMS.Max, int64(40))
	classifierEqual(t, r.Usage, map[string]int64{"input_tokens": 480, "output_tokens": 48})
	classifierEqual(t, r.Agreement, protocol.ClassifierAgreement{Compared: 2, Agreed: 1, Disagreed: 1, Unclaimed: 2})
	classifierEqual(t, f.predictionRows(t, at), before)
}

type reportSeed struct {
	id      string
	at      time.Time
	latency int64
}

func seedClaimedPrediction(t *testing.T, f *classifierFixture, seed reportSeed) {
	t.Helper()
	f.send(t, protocol.Message{MessageID: seed.id})
	f.drain(t, seed.id)
	f.seedPrediction(t, store.Prediction{MessageID: seed.id, Outcome: store.PredictionPredicted, AgentID: "tester", Provider: "typesafe", Model: "jev-latest", ModelVersion: "jev-1.14.0", Threshold: .7, Usage: map[string]int64{"input_tokens": 120, "output_tokens": 12}, LatencyMS: seed.latency, CreatedAt: seed.at})
	if seed.id == "unclaimed" {
		return
	}
	claim := store.ClaimRequest{MessageID: seed.id, Agent: "tester", Task: protocol.TaskReply, Role: protocol.RoleOwner, At: seed.at}
	if seed.id == "active-disagree" {
		claim.Agent = "builder"
	}
	_, err := f.h.store.SetClaim(f.ctx, claim)
	classifierOK(t, err)
	finishReportClaim(t, f, claim)
}
func finishReportClaim(t *testing.T, f *classifierFixture, claim store.ClaimRequest) {
	t.Helper()
	var err error
	switch claim.MessageID {
	case "done-agree":
		_, err = f.h.store.FinishClaim(f.ctx, claim)
	case "released":
		_, err = f.h.store.ReleaseClaim(f.ctx, claim)
	}
	classifierOK(t, err)
}
func (f *classifierFixture) seedPrediction(t *testing.T, p store.Prediction) {
	t.Helper()
	_, err := f.h.store.RecordPrediction(f.ctx, p)
	classifierOK(t, err)
}
func (f *classifierFixture) predictionRows(t *testing.T, since time.Time) []store.Prediction {
	t.Helper()
	rows, _, err := f.h.store.PredictionsSince(f.ctx, since, 100)
	classifierOK(t, err)
	return rows
}
func (f *classifierFixture) report(t *testing.T, since time.Time) *protocol.ClassifierReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	out := f.h.SubmitOperator(ctx, 0, protocol.Message{Type: protocol.TypeOperatorClassifierReport, ClassifierRequest: &protocol.ClassifierReportRequest{Since: since}})
	classifierEqual(t, out.Type, protocol.TypeOperatorClassifierReport)
	classifierEqual(t, out.ClassifierReport != nil, true)
	return out.ClassifierReport
}

func TestClassifierReportAcceptanceTruncationKeepsNewestWindow(t *testing.T) {
	f := newClassifierFixture(t, nil)
	at := time.Now().UTC().Add(-time.Hour)
	for i := 0; i <= protocol.ClassifierReportMax; i++ {
		f.seedPrediction(t, store.Prediction{MessageID: fmt.Sprintf("item-%04d", i), Outcome: store.PredictionSkipped, Reason: "tagged", CreatedAt: at.Add(time.Duration(i) * time.Second)})
	}
	r := f.report(t, at)
	classifierEqual(t, r.Truncated, true)
	classifierEqual(t, r.Count, protocol.ClassifierReportMax)
	classifierEqual(t, len(r.Predictions), protocol.ClassifierReportListed)
	classifierEqual(t, r.Predictions[0].MessageID, fmt.Sprintf("item-%04d", protocol.ClassifierReportMax))
}
