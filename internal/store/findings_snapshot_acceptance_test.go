package store_test

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

const snapshotFindings = `{"total":1,"items":[{"path":"src/check.go","line":9,"summary":"check ownership","truncated":false}]}`

func snapshotOutcome(t *testing.T, o messaging.ReviewOutcome, envelope string) messaging.ReviewOutcome {
	t.Helper()
	var err error
	o.Findings, err = messaging.DecodeFindings([]byte(envelope))
	pairOK(t, err)
	return o
}

type snapshotState struct {
	Raw                  sql.NullString
	Seq, Attempt, Intent sql.NullInt64
	Digest               []byte
}

func snapshotRead(t *testing.T, f workspaceStoreFixture) snapshotState {
	t.Helper()
	var s snapshotState
	pairOK(t, f.raw.QueryRow(`SELECT findings, findings_seq, findings_attempt, findings_intent, findings_digest FROM review_asks_out WHERE contact=? AND ask_id='ask'`, f.key).Scan(&s.Raw, &s.Seq, &s.Attempt, &s.Intent, &s.Digest))
	return s
}

func snapshotAccept(t *testing.T, f workspaceStoreFixture, o messaging.ReviewOutcome) messaging.OutcomeAnswer {
	t.Helper()
	a, err := f.db.PeerMessages().AcceptOutcome(context.Background(), f.key, o, messageAt)
	pairOK(t, err)
	return a
}

func snapshotSubmitted(t *testing.T, f workspaceStoreFixture) messaging.ReviewOutcome {
	t.Helper()
	o := queuedAgentAsk(t, f)
	o.State, o.Attempt, o.Intent, o.Verdict = "submitted", 1, 1, "comment"
	o = snapshotOutcome(t, o, snapshotFindings)
	pairEqual(t, snapshotAccept(t, f, o).Stored, true)
	return o
}

func snapshotUnchanged(t *testing.T, f workspaceStoreFixture, want snapshotState) {
	t.Helper()
	if got := snapshotRead(t, f); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot changed: got %+v want %+v", got, want)
	}
}

func TestFindingsSnapshotAcceptancePersistsAcceptedOutcome(t *testing.T) {
	f := workspaceStore(t)
	o := queuedAgentAsk(t, f)
	o.State, o.Attempt, o.Verdict = "completed", 1, "comment"
	o = snapshotOutcome(t, o, snapshotFindings)
	a, err := f.db.PeerMessages().AcceptOutcome(context.Background(), f.key, o, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	var raw sql.NullString
	var seq, attempt, intent int64
	var digest []byte
	pairOK(t, f.raw.QueryRow(`SELECT findings, findings_seq, findings_attempt, findings_intent, findings_digest FROM review_asks_out WHERE contact=? AND ask_id=?`, f.key, o.AskID).Scan(&raw, &seq, &attempt, &intent, &digest))
	pairEqual(t, raw.Valid, true)
	pairEqual(t, raw.String, snapshotFindings)
	pairEqual(t, seq, int64(o.Seq))
	pairEqual(t, attempt, int64(o.Attempt))
	pairEqual(t, intent, int64(o.Intent))
	sum := o.Digest()
	pairEqual(t, string(digest), string(sum[:]))
}

func TestFindingsSnapshotAcceptanceAbsentProgressKeepsAttributionAndNewAttemptClears(t *testing.T) {
	f := workspaceStore(t)
	o := snapshotSubmitted(t, f)
	first := snapshotRead(t, f)
	o.Seq, o.State, o.Findings = 2, "publishing", nil
	pairEqual(t, snapshotAccept(t, f, o).Stored, true)
	snapshotUnchanged(t, f, first)
	o.Seq, o.Attempt, o.State = 3, 2, "publication_failed"
	pairEqual(t, snapshotAccept(t, f, o).Stored, true)
	cleared := snapshotRead(t, f)
	pairEqual(t, cleared.Raw.Valid, false)
	pairEqual(t, cleared.Seq.Int64, int64(3))
	pairEqual(t, cleared.Attempt.Int64, int64(2))
	sum := o.Digest()
	pairEqual(t, string(cleared.Digest), string(sum[:]))
}

func TestFindingsSnapshotAcceptanceNewerKnownEnvelopeRebindsEvenWhenIdentical(t *testing.T) {
	f := workspaceStore(t)
	o := snapshotSubmitted(t, f)
	o.Seq, o.State = 2, "publishing"
	pairEqual(t, snapshotAccept(t, f, o).Stored, true)
	s := snapshotRead(t, f)
	pairEqual(t, s.Raw.String, snapshotFindings)
	pairEqual(t, s.Seq.Int64, int64(2))
	sum := o.Digest()
	pairEqual(t, string(s.Digest), string(sum[:]))
}

func TestFindingsSnapshotAcceptanceSameSequenceEnrichesOnceAndConflictsAtomically(t *testing.T) {
	f := workspaceStore(t)
	o := queuedAgentAsk(t, f)
	o.State, o.Attempt, o.Verdict = "completed", 1, "comment"
	pairEqual(t, snapshotAccept(t, f, o).Stored, true)
	pairEqual(t, snapshotRead(t, f).Raw.Valid, false)
	o = snapshotOutcome(t, o, snapshotFindings)
	pairEqual(t, snapshotAccept(t, f, o).Stored, true)
	before := reviewIDSnapshot(t, f)
	_, err := f.raw.Exec(`CREATE TRIGGER no_findings_rewrite BEFORE UPDATE OF findings ON review_asks_out BEGIN SELECT RAISE(ABORT,'replay wrote findings'); END`)
	pairOK(t, err)
	pairEqual(t, snapshotAccept(t, f, o).Stored, true)
	_, err = f.raw.Exec(`DROP TRIGGER no_findings_rewrite`)
	pairOK(t, err)
	conflict := snapshotOutcome(t, o, strings.Replace(snapshotFindings, "check ownership", "different finding", 1))
	a := snapshotAccept(t, f, conflict)
	pairEqual(t, a.Stored, false)
	pairEqual(t, a.Reason, messaging.ReasonConflict)
	reviewIDUnchanged(t, f, before)
	o.Findings = nil
	pairEqual(t, snapshotAccept(t, f, o).Stored, true)
	reviewIDUnchanged(t, f, before)
}

func TestFindingsSnapshotAcceptanceReplayCannotReplaceRetainedEarlierSnapshot(t *testing.T) {
	f := workspaceStore(t)
	o := snapshotSubmitted(t, f)
	o.Seq, o.State, o.Findings = 2, "publishing", nil
	pairEqual(t, snapshotAccept(t, f, o).Stored, true)
	before := reviewIDSnapshot(t, f)
	o = snapshotOutcome(t, o, strings.Replace(snapshotFindings, "check ownership", "different finding", 1))
	a := snapshotAccept(t, f, o)
	pairEqual(t, a.Stored, false)
	pairEqual(t, a.Reason, messaging.ReasonConflict)
	reviewIDUnchanged(t, f, before)
}

func TestFindingsSnapshotAcceptanceRejectedAndOlderOutcomesNeverChangeSnapshot(t *testing.T) {
	for _, kind := range []string{"reviewer", "contact", "conflict", "invalid-envelope", "older"} {
		t.Run(kind, func(t *testing.T) {
			f := workspaceStore(t)
			o := snapshotSubmitted(t, f)
			o.Seq, o.State = 2, "publishing"
			pairEqual(t, snapshotAccept(t, f, o).Stored, true)
			before := reviewIDSnapshot(t, f)
			contact := f.key
			switch kind {
			case "reviewer":
				o.Reviewer = "wrong-reviewer"
			case "contact":
				contact = messageContact(t, f.db, 2)
			case "conflict":
				o.Summary = "conflicting core"
			case "invalid-envelope":
				o.Seq = 3
				o.ReviewID = "must-not-stick"
				o.Findings.Items[0].Line = -1
			case "older":
				o.Seq = 1
			}
			a, err := f.db.PeerMessages().AcceptOutcome(context.Background(), contact, o, messageAt)
			pairOK(t, err)
			pairEqual(t, a.Stored, kind == "older")
			reviewIDUnchanged(t, f, before)
		})
	}
}

func TestFindingsSnapshotAcceptanceWriteAndNoticeFailuresRollBackTogether(t *testing.T) {
	for _, table := range []string{"review_asks_out", "remote_imports"} {
		t.Run(table, func(t *testing.T) {
			f := workspaceStore(t)
			o := queuedAgentAsk(t, f)
			o.State, o.Attempt, o.Verdict = "completed", 1, "comment"
			o = snapshotOutcome(t, o, snapshotFindings)
			before := reviewIDSnapshot(t, f)
			event := "INSERT"
			if table == "review_asks_out" {
				event = "UPDATE OF findings"
			}
			_, err := f.raw.Exec("CREATE TRIGGER fail_findings BEFORE " + event + " ON " + table + " BEGIN SELECT RAISE(ABORT,'injected'); END")
			pairOK(t, err)
			_, err = f.db.PeerMessages().AcceptOutcome(context.Background(), f.key, o, messageAt)
			if err == nil {
				t.Fatal("failed transaction reported success")
			}
			reviewIDUnchanged(t, f, before)
			_, err = f.raw.Exec(`DROP TRIGGER fail_findings`)
			pairOK(t, err)
			pairEqual(t, snapshotAccept(t, f, o).Stored, true)
			pairEqual(t, snapshotRead(t, f).Raw.String, snapshotFindings)
		})
	}
}

func snapshotReopen(t *testing.T, f *workspaceStoreFixture) {
	t.Helper()
	var n int
	var name, path string
	pairOK(t, f.raw.QueryRow(`PRAGMA database_list`).Scan(&n, &name, &path))
	pairOK(t, f.db.Close())
	db, err := store.Open(path)
	pairOK(t, err)
	t.Cleanup(func() { _ = db.Close() })
	f.db = db
}

func TestFindingsSnapshotAcceptanceReopenAndGrantChangesPreserveSnapshot(t *testing.T) {
	f := workspaceStore(t)
	snapshotSubmitted(t, f)
	before := snapshotRead(t, f)
	snapshotReopen(t, &f)
	snapshotUnchanged(t, f, before)
	_, err := f.db.PeerMessages().Unshare(context.Background(), f.key, []string{"sender"}, messageAt)
	pairOK(t, err)
	_, err = f.db.PeerMessages().ReplacePeerDirectory(context.Background(), f.key, nil, messageAt)
	pairOK(t, err)
	snapshotUnchanged(t, f, before)
}

func TestFindingsSnapshotAcceptanceRetentionFollowsAskRow(t *testing.T) {
	f := workspaceStore(t)
	o := outcomeAsk(t, f.db, f.key) // Operator request: no unacknowledged agent notice protects it.
	o.State, o.Attempt, o.Verdict = "completed", 1, "comment"
	o = snapshotOutcome(t, o, snapshotFindings)
	pairEqual(t, snapshotAccept(t, f, o).Stored, true)
	pairOK(t, f.db.PeerMessages().Collect(context.Background(), messageAt.Add(store.AskRetention-time.Hour)))
	pairEqual(t, snapshotRead(t, f).Raw.Valid, true)
	pairOK(t, f.db.PeerMessages().Collect(context.Background(), messageAt.Add(store.AskRetention+time.Hour)))
	_, found, err := f.db.PeerMessages().AskOutFor(context.Background(), f.key, o.AskID)
	pairOK(t, err)
	pairEqual(t, found, false)
}
