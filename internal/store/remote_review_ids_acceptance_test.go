package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func storedReviewID(t *testing.T, f workspaceStoreFixture) string {
	t.Helper()
	var id string
	pairOK(t, f.raw.QueryRow(`SELECT COALESCE(review_id,'') FROM review_asks_out WHERE contact=? AND ask_id='ask'`, f.key).Scan(&id))
	return id
}

func reviewIDTable(t *testing.T, f workspaceStoreFixture, query string) string {
	t.Helper()
	rows, err := f.raw.Query(query)
	pairOK(t, err)
	defer rows.Close()
	columns, err := rows.Columns()
	pairOK(t, err)
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		pairOK(t, rows.Scan(pointers...))
		result = append(result, values)
	}
	pairOK(t, rows.Err())
	encoded, err := json.Marshal(result)
	pairOK(t, err)
	return string(encoded)
}

func reviewIDSnapshot(t *testing.T, f workspaceStoreFixture) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []string{"review_asks_out", "peer_messages", "remote_imports", "messages", "durable_inbox", "review_outcomes_out"} {
		result[table] = reviewIDTable(t, f, "SELECT * FROM "+table+" ORDER BY rowid")
	}
	return result
}

func reviewIDUnchanged(t *testing.T, f workspaceStoreFixture, before map[string]string) {
	t.Helper()
	after := reviewIDSnapshot(t, f)
	for table, old := range before {
		pairEqual(t, after[table], old)
	}
}

func acceptReviewID(t *testing.T, f workspaceStoreFixture, o messaging.ReviewOutcome) messaging.OutcomeAnswer {
	t.Helper()
	a, err := f.db.PeerMessages().AcceptOutcome(context.Background(), f.key, o, messageAt.Add(time.Hour))
	pairOK(t, err)
	return a
}

func reviewIDRejectedUnchanged(t *testing.T, f workspaceStoreFixture, o messaging.ReviewOutcome, reason string, before map[string]string) {
	t.Helper()
	a := acceptReviewID(t, f, o)
	pairEqual(t, a.Stored, false)
	pairEqual(t, a.Reason, reason)
	reviewIDUnchanged(t, f, before)
}

func reviewIDPending(t *testing.T, f workspaceStoreFixture) messaging.ReviewOutcome {
	t.Helper()
	rows, err := f.db.PeerMessages().PendingOutcomes(context.Background(), f.key, 10)
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
	return rows[0]
}

func reviewIDOfferUnchanged(t *testing.T, f workspaceStoreFixture, old messaging.ReviewOutcome, id, payload string) {
	t.Helper()
	offer := reviewIDPending(t, f)
	pairEqual(t, offer.ReviewID, id)
	pairEqual(t, offer.Seq, old.Seq)
	pairEqual(t, offer.Digest(), old.Digest())
	pairEqual(t, reviewIDPayload(t, f), payload)
}

func TestRemoteReviewIDsStoreAcceptanceUpgradeReplayFillsAndDowngradeNeverClears(t *testing.T) {
	f := workspaceStore(t)
	o := queuedAgentAsk(t, f)
	pairEqual(t, acceptReviewID(t, f, o).Stored, true)
	pairEqual(t, storedReviewID(t, f), "")
	stateQuery := `SELECT state,seq,digest,attempt,intent,verdict,summary,url,updated_at,settled_at FROM review_asks_out ORDER BY contact,ask_id`
	before := reviewIDTable(t, f, stateQuery)
	o.ReviewID = "reviewer-local"
	pairEqual(t, acceptReviewID(t, f, o).Stored, true)
	pairEqual(t, storedReviewID(t, f), o.ReviewID)
	pairEqual(t, reviewIDTable(t, f, stateQuery), before)
	filled := reviewIDSnapshot(t, f)
	pairEqual(t, acceptReviewID(t, f, o).Stored, true)
	reviewIDUnchanged(t, f, filled)
	o.ReviewID = ""
	pairEqual(t, acceptReviewID(t, f, o).Stored, true)
	pairEqual(t, storedReviewID(t, f), "reviewer-local")
	reviewIDUnchanged(t, f, filled)
}

func TestRemoteReviewIDsStoreAcceptanceConflictPrecedesReplayAndStateMutation(t *testing.T) {
	for _, seq := range []uint64{1, 2} {
		t.Run(fmt.Sprint(seq), func(t *testing.T) {
			f := workspaceStore(t)
			o := queuedAgentAsk(t, f)
			o.ReviewID = "first-id"
			pairEqual(t, acceptReviewID(t, f, o).Stored, true)
			before := reviewIDSnapshot(t, f)
			o.ReviewID = "conflicting-id"
			if seq == 2 {
				o.Seq, o.Attempt, o.State = 2, 1, "accepted"
			}
			reviewIDRejectedUnchanged(t, f, o, messaging.ReasonConflict, before)
		})
	}
}

func TestRemoteReviewIDsStoreAcceptanceInvalidReviewerCannotBindID(t *testing.T) {
	f := workspaceStore(t)
	o := queuedAgentAsk(t, f)
	o.Reviewer, o.ReviewID = "intruder", "stolen-id"
	a := acceptReviewID(t, f, o)
	pairEqual(t, a.Stored, false)
	pairEqual(t, storedReviewID(t, f), "")
	o.Reviewer, o.ReviewID = "target", "actual-id"
	pairEqual(t, acceptReviewID(t, f, o).Stored, true)
	pairEqual(t, storedReviewID(t, f), "actual-id")
	o.Reviewer, o.ReviewID = "intruder", "different-id"
	a = acceptReviewID(t, f, o)
	pairEqual(t, a.Stored, false)
	pairEqual(t, a.Reason, messaging.ReasonInvalid)
	pairEqual(t, storedReviewID(t, f), "actual-id")
}

func TestRemoteReviewIDsStoreAcceptanceOlderSequenceConflictIsNeverAcknowledged(t *testing.T) {
	f := workspaceStore(t)
	o := queuedAgentAsk(t, f)
	o.Seq, o.Attempt, o.State, o.ReviewID = 2, 1, "accepted", "first-id"
	pairEqual(t, acceptReviewID(t, f, o).Stored, true)
	before := reviewIDSnapshot(t, f)
	o.Seq, o.Attempt, o.State, o.ReviewID = 1, 0, "requested", "different-id"
	reviewIDRejectedUnchanged(t, f, o, messaging.ReasonConflict, before)
}

func TestRemoteReviewIDsStoreAcceptanceSameAskAcrossContactsBindsIndependently(t *testing.T) {
	f := workspaceStore(t)
	o := queuedAgentAsk(t, f)
	o.ReviewID = "first-contact-id"
	pairEqual(t, acceptReviewID(t, f, o).Stored, true)
	other := messageContact(t, f.db, 2)
	m := remoteAsk("ask")
	_, err := f.db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: other, ID: "ask", Text: m.Text, To: []string{"target"}, Review: m.Review, Now: messageAt})
	pairOK(t, err)
	o.ReviewID = "other-contact-id"
	a, err := f.db.PeerMessages().AcceptOutcome(context.Background(), other, o, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	pairEqual(t, storedReviewID(t, f), "first-contact-id")
	second := f
	second.key = other
	pairEqual(t, storedReviewID(t, second), "other-contact-id")
}

func reviewIDFailedTransaction(t *testing.T, replay bool) {
	t.Helper()
	f := workspaceStore(t)
	o := queuedAgentAsk(t, f)
	trigger := `CREATE TRIGGER fail_id BEFORE INSERT ON remote_imports BEGIN SELECT RAISE(ABORT,'injected notice failure'); END`
	if replay {
		pairEqual(t, acceptReviewID(t, f, o).Stored, true)
		trigger = `CREATE TRIGGER fail_id BEFORE UPDATE OF review_id ON review_asks_out BEGIN SELECT RAISE(ABORT,'injected ID fill failure'); END`
	}
	o.ReviewID = "bound-only-on-commit"
	before := reviewIDSnapshot(t, f)
	_, err := f.raw.Exec(trigger)
	pairOK(t, err)
	a, err := f.db.PeerMessages().AcceptOutcome(context.Background(), f.key, o, messageAt)
	if err == nil || a.Stored {
		t.Fatal("outcome reported stored despite failed transaction")
	}
	reviewIDUnchanged(t, f, before)
	_, err = f.raw.Exec(`DROP TRIGGER fail_id`)
	pairOK(t, err)
	pairEqual(t, acceptReviewID(t, f, o).Stored, true)
	pairEqual(t, storedReviewID(t, f), o.ReviewID)
}

func TestRemoteReviewIDsStoreAcceptanceFailedNoticeRollsBackIDAndAcceptance(t *testing.T) {
	reviewIDFailedTransaction(t, false)
}

func TestRemoteReviewIDsStoreAcceptanceFailedReplayFillDoesNotClaimStored(t *testing.T) {
	reviewIDFailedTransaction(t, true)
}

// This is the persisted v5 JSON shape, not a v6 outcome projected on wire.
type legacyReviewIDOutcome struct {
	AskID, Reviewer                      string
	Seq                                  uint64
	Attempt, Intent                      uint32
	State, Verdict, Summary, URL, Reason string
}

func reviewIDSource(t *testing.T) (workspaceStoreFixture, store.ImportResult) {
	t.Helper()
	db, raw, key := messageRawFixture(t)
	routingRegister(t, db, "reviewer")
	reviewGrant(t, db, key, store.ReviewAuto)
	m := remoteAsk("source-ask")
	a, err := db.PeerMessages().Accept(context.Background(), key, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	got, err := db.ImportRemote(context.Background(), store.RemoteImport{Contact: key, RemoteID: m.ID}, 1, messageAt)
	pairOK(t, err)
	pairEqual(t, got.State, store.ImportImported)
	return workspaceStoreFixture{db, raw, key}, got
}

func reviewIDPayload(t *testing.T, f workspaceStoreFixture) string {
	t.Helper()
	var payload string
	pairOK(t, f.raw.QueryRow(`SELECT payload FROM review_outcomes_out WHERE contact=? AND ask_id='source-ask'`, f.key).Scan(&payload))
	return payload
}

func TestRemoteReviewIDsStoreAcceptanceActualOldPayloadEnrichesWithoutRewrite(t *testing.T) {
	f, imported := reviewIDSource(t)
	var legacy legacyReviewIDOutcome
	pairOK(t, json.Unmarshal([]byte(reviewIDPayload(t, f)), &legacy))
	legacy.Summary = "review_id fake-from-prose"
	oldPayload, err := json.Marshal(legacy)
	pairOK(t, err)
	_, err = f.raw.Exec(`UPDATE review_outcomes_out SET payload=? WHERE contact=? AND ask_id='source-ask'`, string(oldPayload), f.key)
	pairOK(t, err)
	var old messaging.ReviewOutcome
	pairOK(t, json.Unmarshal(oldPayload, &old))
	reviewIDOfferUnchanged(t, f, old, imported.ReviewID, string(oldPayload))
	// A same-ask review from another contact cannot supply this ID.
	rec, found, err := f.db.GetReview(context.Background(), imported.ReviewID)
	pairOK(t, err)
	pairEqual(t, found, true)
	rec.View.ID = "other-contact-local-review"
	rec.View.Remote.Contact = messageContact(t, f.db, 2)
	_, _, err = f.db.CreateReview(context.Background(), rec, store.ReviewAction{Actor: "test", Action: "fixture"})
	pairOK(t, err)
	_, err = f.raw.Exec(`DELETE FROM reviews WHERE review_id=?`, imported.ReviewID)
	pairOK(t, err)
	reviewIDOfferUnchanged(t, f, old, "", string(oldPayload))
}

func TestRemoteReviewIDsStoreAcceptanceNewQueuedPayloadKeepsIDAtOfferBoundary(t *testing.T) {
	f, imported := reviewIDSource(t)
	var fields map[string]json.RawMessage
	pairOK(t, json.Unmarshal([]byte(reviewIDPayload(t, f)), &fields))
	for _, key := range []string{"ReviewID", "review_id"} {
		if _, present := fields[key]; present {
			t.Fatalf("offer-time review ID field %q was persisted in queued payload", key)
		}
	}
	pairEqual(t, reviewIDPending(t, f).ReviewID, imported.ReviewID)
}

func TestRemoteReviewIDsStoreAcceptancePreReviewOutcomesHaveNoInventedID(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			db, key := messageFixture(t)
			reviewGrant(t, db, key, store.ReviewAsk)
			a, err := db.PeerMessages().Accept(context.Background(), key, remoteAsk("pending-ask"), messageAt)
			pairOK(t, err)
			pairEqual(t, a.Stored, true)
			if revoke {
				reviewGrant(t, db, key, store.ReviewOff)
			}
			rows, err := db.PeerMessages().PendingOutcomes(context.Background(), key, 10)
			pairOK(t, err)
			pairEqual(t, len(rows), 1)
			pairEqual(t, rows[0].ReviewID, "")
			want := "waiting"
			if revoke {
				want = "withheld"
			}
			pairEqual(t, rows[0].State, want)
		})
	}
}
