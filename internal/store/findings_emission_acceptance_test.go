package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
	"github.com/pairtask/pairtask/internal/store"
)

func emissionComplete(t *testing.T, f workspaceStoreFixture, id string, findings []protocol.Finding) error {
	t.Helper()
	_, _, err := f.db.ChangeReview(context.Background(), id, store.ReviewAction{Actor: "reviewer", Action: "verdict"}, func(rec review.Record) (review.Record, bool, error) {
		rec.View.State, rec.View.Attempt = protocol.ReviewCompleted, 1
		rec.View.Verdict = &protocol.Verdict{Outcome: protocol.OutcomeComment, Summary: "reviewed", Findings: findings}
		rec.View.Publication = protocol.Publication{State: protocol.PublicationNone}
		rec.View.UpdatedAt = messageAt.Add(time.Minute)
		return rec, true, nil
	})
	return err
}

func emissionPending(t *testing.T, f workspaceStoreFixture) messaging.ReviewOutcome {
	t.Helper()
	rows, err := f.db.PeerMessages().PendingOutcomes(context.Background(), f.key, 10)
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
	return rows[0]
}

func TestFindingsEmissionAcceptanceVerdictProjectionIsQueuedAndResentUnchanged(t *testing.T) {
	f, imported := reviewIDSource(t)
	pairEqual(t, emissionPending(t, f).Findings == nil, true)
	full := []protocol.Finding{{Path: "src/check.go", Line: 9, Summary: strings.Repeat("界", 200)}}
	pairOK(t, emissionComplete(t, f, imported.ReviewID, full))
	o := emissionPending(t, f)
	if o.Findings == nil {
		t.Fatal("verdict findings not projected into outcome")
	}
	pairEqual(t, o.Findings.Total, 1)
	pairEqual(t, o.Findings.Items[0].Path, full[0].Path)
	pairEqual(t, o.Findings.Items[0].Truncated, true)
	if len(o.Findings.Items[0].Summary) > 512 {
		t.Fatal("emitted summary exceeds projection limit")
	}
	payload := reviewIDPayload(t, f)
	wire, err := messaging.EncodeOutcomeFor(o, 7)
	pairOK(t, err)
	_, err = f.raw.Exec(`UPDATE reviews SET doc=json_set(doc,'$.verdict.findings',json(?)) WHERE review_id=?`, `[{"path":"changed.go","line":2,"summary":"later data"}]`, imported.ReviewID)
	pairOK(t, err)
	snapshotReopen(t, &f)
	again, err := messaging.EncodeOutcomeFor(emissionPending(t, f), 7)
	pairOK(t, err)
	if !bytes.Equal(wire, again) {
		t.Fatal("resend rebuilt findings from changed review record")
	}
	pairEqual(t, reviewIDPayload(t, f), payload)
	var stored messaging.ReviewOutcome
	pairOK(t, json.Unmarshal([]byte(payload), &stored))
	if stored.Findings == nil {
		t.Fatal("queued payload did not durably contain findings")
	}
}

func TestFindingsEmissionAcceptanceNoVerdictAndKnownEmptyRemainDistinct(t *testing.T) {
	f, imported := reviewIDSource(t)
	pairEqual(t, emissionPending(t, f).Findings == nil, true)
	pairOK(t, emissionComplete(t, f, imported.ReviewID, []protocol.Finding{}))
	got := emissionPending(t, f)
	if got.Findings == nil {
		t.Fatal("empty verdict was treated as unavailable")
	}
	wire, err := messaging.EncodeFindings(got.Findings)
	pairOK(t, err)
	pairEqual(t, string(wire), `{"total":0,"items":[]}`)
}

func TestFindingsEmissionAcceptanceOutcomeWriteFailureRollsBackVerdict(t *testing.T) {
	f, imported := reviewIDSource(t)
	before := reviewIDPayload(t, f)
	_, err := f.raw.Exec(`CREATE TRIGGER fail_emission BEFORE INSERT ON review_outcomes_out BEGIN SELECT RAISE(ABORT,'injected'); END`)
	pairOK(t, err)
	err = emissionComplete(t, f, imported.ReviewID, []protocol.Finding{{Path: "x.go", Line: 1, Summary: "note"}})
	if err == nil {
		t.Fatal("failed findings outcome write committed review")
	}
	row, found, err := f.db.GetReview(context.Background(), imported.ReviewID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, row.View.Verdict == nil, true)
	pairEqual(t, reviewIDPayload(t, f), before)
}
