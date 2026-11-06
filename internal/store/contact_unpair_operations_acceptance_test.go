package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func unpairRequest(t *testing.T, f *operatorContactFixture, id string) store.ContactUnpair {
	t.Helper()
	var epoch int64
	pairOK(t, f.raw.QueryRow("SELECT pairing_epoch FROM peer_contacts WHERE identity_key=?", f.contact).Scan(&epoch))
	return store.ContactUnpair{OperationID: id, Contact: f.contact, Confirm: f.contact, SeenEpoch: epoch,
		AuditID: f.audit(t, contactAuditInput{"unpair", id}), At: messageAt}
}

func unpairSnapshot(t *testing.T, f *operatorContactFixture) map[string]epochTableSnapshot {
	t.Helper()
	tables := map[string]int{}
	for _, name := range epochRebuildOrder {
		tables[name] = 0
	}
	for _, name := range []string{"peer_contacts", "peer_pairing_requests", "peer_joins", "pairing_exchanges", "pairing_marks",
		"operator_contact_requests", "contact_unpair_results", "contact_notices_out", "operator_audit", "contact_address_ops"} {
		tables[name] = 0
	}
	return epochSnapshot(t, f.raw, epochFixtureManifest{Tables: tables})
}

func TestContactUnpairOperationsAcceptanceExactCurrentConfirmationRequired(t *testing.T) {
	for _, kind := range []string{"missing", "prefix", "wrong", "stale"} {
		t.Run(kind, func(t *testing.T) {
			f := newOperatorContactFixture(t)
			r := unpairRequest(t, f, "confirm-"+kind)
			switch kind {
			case "missing":
				r.Confirm = ""
			case "prefix":
				r.Confirm = r.Contact[:len(r.Contact)-1]
			case "wrong":
				r.Confirm = "sha256:another-identity"
			case "stale":
				r.SeenEpoch++
			}
			before := unpairSnapshot(t, f)
			_, _, err := f.db.UnpairContact(context.Background(), r)
			pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
			if !reflect.DeepEqual(before, unpairSnapshot(t, f)) {
				t.Fatal("refused confirmation changed contact authority or audit state")
			}
		})
	}
}

func TestContactUnpairOperationsAcceptanceReplayAndNoOpSurviveRestart(t *testing.T) {
	f := newOperatorContactFixture(t)
	r := unpairRequest(t, f, "unpair-once")
	first, applied, err := f.db.UnpairContact(context.Background(), r)
	pairOK(t, err)
	pairEqual(t, applied, true)
	pairEqual(t, first.Changed, true)
	pairEqual(t, first.Generation, int64(1))
	pairEqual(t, first.Epoch, r.SeenEpoch)
	pairEqual(t, first.UnpairedAt.Equal(messageAt), true)
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	replayed, applied, err := f.db.UnpairContact(context.Background(), r)
	pairOK(t, err)
	pairEqual(t, replayed, first)
	pairEqual(t, applied, false)
	noop := unpairRequest(t, f, "unpair-already-unpaired")
	beforeGeneration := epochCount(t, f.raw, "SELECT generation FROM peer_contacts WHERE identity_key=?", f.contact)
	beforeMark := epochCount(t, f.raw, "SELECT mark FROM pairing_marks WHERE id=1")
	beforeNotices := epochRows(t, f.raw, "contact_notices_out", epochColumns(t, f.raw, "contact_notices_out"))
	result, _, err := f.db.UnpairContact(context.Background(), noop)
	pairOK(t, err)
	pairEqual(t, result.Changed, false)
	pairEqual(t, epochCount(t, f.raw, "SELECT generation FROM peer_contacts WHERE identity_key=?", f.contact), beforeGeneration)
	pairEqual(t, epochCount(t, f.raw, "SELECT mark FROM pairing_marks WHERE id=1"), beforeMark)
	if !reflect.DeepEqual(beforeNotices, epochRows(t, f.raw, "contact_notices_out", epochColumns(t, f.raw, "contact_notices_out"))) {
		t.Fatal("re-unpair changed notice delivery state")
	}
	_, err = f.db.PeerMessages().Admit(context.Background(), f.contact)
	pairEqual(t, errors.Is(err, store.ErrUnpaired), true)
}

func TestContactUnpairOperationsAcceptanceAuditFailureRollsBack(t *testing.T) {
	f := pauseFenceFixture(t)
	r := unpairRequest(t, f, "atomic-unpair")
	_, err := f.raw.Exec("CREATE TRIGGER fail_unpair_audit BEFORE UPDATE ON operator_audit WHEN NEW.result='accepted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END")
	pairOK(t, err)
	before := unpairSnapshot(t, f)
	_, _, err = f.db.UnpairContact(context.Background(), r)
	if err == nil {
		t.Fatal("unpair committed despite audit failure")
	}
	if !reflect.DeepEqual(before, unpairSnapshot(t, f)) {
		t.Fatal("failed unpair changed authority, queues, evidence or audit")
	}
	_, err = f.raw.Exec("DROP TRIGGER fail_unpair_audit")
	pairOK(t, err)
	_, _, err = f.db.UnpairContact(context.Background(), r)
	pairOK(t, err)
}

func TestContactUnpairOperationsAcceptanceOperationFingerprintCannotChange(t *testing.T) {
	f := newOperatorContactFixture(t)
	r := unpairRequest(t, f, "fingerprinted-unpair")
	_, _, err := f.db.UnpairContact(context.Background(), r)
	pairOK(t, err)
	other := messageContact(t, f.db, 2)
	for _, kind := range []string{"contact", "confirmation", "epoch"} {
		t.Run(kind, func(t *testing.T) {
			altered := r
			switch kind {
			case "contact":
				altered.Contact, altered.Confirm = other, other
			case "confirmation":
				altered.Confirm = "different-confirmation"
			case "epoch":
				altered.SeenEpoch++
			}
			before := unpairSnapshot(t, f)
			_, _, err := f.db.UnpairContact(context.Background(), altered)
			pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
			if !reflect.DeepEqual(before, unpairSnapshot(t, f)) {
				t.Fatal("operation-ID conflict changed pairing state")
			}
		})
	}
	// Check action reuse on a still-active contact, independently of
	// already-unpaired eligibility in the existing pause endpoint.
	active := newOperatorContactFixture(t)
	pause := pauseRequest(t, active, "same-id-another-action", false)
	_, err = active.db.SetContactPaused(context.Background(), pause)
	pairOK(t, err)
	_, _, err = active.db.UnpairContact(context.Background(), unpairRequest(t, active, pause.OperationID))
	pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
}

func TestContactUnpairOperationsAcceptanceEveryOldSessionMethodIsFenced(t *testing.T) {
	f := pauseFenceFixture(t)
	bound, err := f.db.PeerMessages().Admit(context.Background(), f.contact)
	pairOK(t, err)
	_, _, err = f.db.UnpairContact(context.Background(), unpairRequest(t, f, "fence-unpair"))
	pairOK(t, err)
	for _, method := range pauseWireMethods() {
		t.Run(method.name, func(t *testing.T) {
			before := unpairSnapshot(t, f)
			err := method.run(bound, f.contact)
			pairEqual(t, errors.Is(err, store.ErrFenced), true)
			if !reflect.DeepEqual(before, unpairSnapshot(t, f)) {
				t.Fatal("old session changed retired pairing state")
			}
		})
	}
	_, err = bound.Admit(context.Background(), f.contact)
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
}
