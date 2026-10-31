package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/store"
)

func addressMove(t *testing.T, f *operatorContactFixture, id, to string) store.ContactAddress {
	t.Helper()
	before := contactGet(t, f.db, f.contact)
	outcome, fresh, err := f.db.BeginContactAddress(context.Background(), store.AddressOp{ID: id, Contact: f.contact, Candidate: to, At: time.Now()})
	pairOK(t, err)
	if fresh || outcome.State != store.AddressRunning {
		t.Fatalf("begin %s: outcome=%+v fresh=%v", id, outcome, fresh)
	}
	return store.ContactAddress{OpID: id, Contact: f.contact, From: before.Destination, FromAt: before.DestinationAt,
		To: to, At: time.Now(), StillDials: func(peer.Contact) bool { return true }}
}

func TestContactAddressAcceptancePreservesIdentityHistoryAndName(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	before := contactGet(t, f.db, f.contact)
	_, err := f.send(t, contactSendInput{"history-before-move", "keep this message"})
	pairOK(t, err)
	_, err = f.db.SetContactLocalName(ctx, store.ContactName{Contact: f.contact, Name: "My peer", AuditID: f.audit(t, contactAuditInput{"name", "rename-before-move"})})
	pairOK(t, err)
	_, err = f.db.PeerMessages().Share(ctx, f.contact, []string{"worker"}, time.Now())
	pairOK(t, err)
	_, err = f.db.PeerMessages().SetReviewGrant(ctx, f.contact, store.ReviewGrantChange{
		Agents: []string{"reviewer"}, Mode: store.ReviewAuto, Publish: true, Formal: true, Now: time.Now()})
	pairOK(t, err)
	move := addressMove(t, f, "move-preserve", "[2001:db8::1]:7445")
	updated, err := f.db.ChangeContactAddress(ctx, move)
	pairOK(t, err)
	pairEqual(t, updated.Destination, move.To)
	pairEqual(t, updated.IdentityKey, before.IdentityKey)
	pairEqual(t, updated.Name, before.Name)
	pairEqual(t, updated.LocalName, "My peer")
	pairEqual(t, updated.Joined, before.Joined)
	pairEqual(t, updated.Invited, before.Invited)
	pairEqual(t, updated.EstablishedAt.Equal(before.EstablishedAt), true)
	pairEqual(t, updated.DestinationAt.After(before.DestinationAt), true)
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), f.contact)), 1)
	assertAddressGrants(t, f)
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	pairEqual(t, contactGet(t, f.db, f.contact).Destination, move.To)
	pairEqual(t, contactGet(t, f.db, f.contact).LocalName, "My peer")
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), f.contact)), 1)
	assertAddressGrants(t, f)
	contactsCount(t, f.db, 1)
}

func assertAddressGrants(t *testing.T, f *operatorContactFixture) {
	t.Helper()
	shared, err := f.db.PeerMessages().SharedAgents(context.Background(), f.contact)
	pairOK(t, err)
	if len(shared) != 1 || shared[0] != "worker" {
		t.Fatalf("address move lost message sharing: %v", shared)
	}
	grants, err := f.db.PeerMessages().ReviewGrants(context.Background(), f.contact)
	pairOK(t, err)
	if len(grants) != 1 || grants[0].Agent != "reviewer" || grants[0].Mode != store.ReviewAuto || !grants[0].Publish || !grants[0].Formal {
		t.Fatalf("address move lost review grant: %+v", grants)
	}
}

func TestContactAddressAcceptanceStaleProbeCannotOverwriteNewerDestination(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	old := addressMove(t, f, "move-A", "older.test:7445")
	newer := addressMove(t, f, "move-B", "newer.test:7445")
	_, err := f.db.ChangeContactAddress(ctx, newer)
	pairOK(t, err)
	_, err = f.db.ChangeContactAddress(ctx, old)
	if !errors.Is(err, peer.ErrDestinationChanged) {
		t.Fatalf("stale probe returned %v, want destination changed", err)
	}
	pairEqual(t, contactGet(t, f.db, f.contact).Destination, newer.To)
	// The stale CAS settles its operation as refused atomically.
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	pairEqual(t, contactGet(t, f.db, f.contact).Destination, newer.To)
	for _, tc := range []struct{ op, candidate, state string }{
		{old.OpID, old.To, store.AddressRefused},
		{newer.OpID, newer.To, store.AddressAccepted},
	} {
		outcome, fresh, err := f.db.BeginContactAddress(ctx, store.AddressOp{ID: tc.op, Contact: f.contact, Candidate: tc.candidate, At: time.Now()})
		pairOK(t, err)
		pairEqual(t, fresh, true)
		pairEqual(t, outcome.State, tc.state)
	}
	pairEqual(t, contactGet(t, f.db, f.contact).Destination, newer.To)
}

func TestContactAddressAcceptanceAcceptedAThenBAfterRestartCannotReplayA(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	original := contactGet(t, f.db, f.contact)
	first := addressMove(t, f, "move-A", "first.test:7445")
	first.At = original.DestinationAt // a stalled clock still advances the CAS timestamp
	one, err := f.db.ChangeContactAddress(ctx, first)
	pairOK(t, err)
	pairEqual(t, one.DestinationAt.Equal(original.DestinationAt.Add(time.Nanosecond)), true)
	second := addressMove(t, f, "move-B", "second.test:7445")
	second.At = one.DestinationAt.Add(-time.Hour) // backward clock, not just frozen
	_, err = f.db.ChangeContactAddress(ctx, second)
	pairOK(t, err)
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	pairEqual(t, contactGet(t, f.db, f.contact).DestinationAt.Equal(one.DestinationAt.Add(time.Nanosecond)), true)
	outcome, known, err := f.db.BeginContactAddress(ctx, store.AddressOp{ID: first.OpID, Contact: f.contact, Candidate: first.To, At: time.Now()})
	pairOK(t, err)
	pairEqual(t, known, true)
	pairEqual(t, outcome.State, store.AddressAccepted)
	_, err = f.db.ChangeContactAddress(ctx, first)
	if !errors.Is(err, store.ErrAddressOpSettled) {
		t.Fatalf("old accepted operation replay returned %v, want settled", err)
	}
	pairEqual(t, contactGet(t, f.db, f.contact).Destination, second.To)
	_, _, err = f.db.BeginContactAddress(ctx, store.AddressOp{ID: first.OpID, Contact: f.contact, Candidate: second.To, At: time.Now()})
	if !errors.Is(err, store.ErrAddressOpConflict) {
		t.Fatalf("same ID, changed candidate returned %v, want conflict", err)
	}
}

func TestContactAddressAcceptanceElectionCheckedAgainAtCommit(t *testing.T) {
	f := newOperatorContactFixture(t)
	move := addressMove(t, f, "move-election", "new.test:443")
	move.StillDials = func(peer.Contact) bool { return false }
	_, err := f.db.ChangeContactAddress(context.Background(), move)
	if !errors.Is(err, peer.ErrNotDialing) {
		t.Fatalf("non-dialer move returned %v, want not dialing", err)
	}
	pairEqual(t, contactGet(t, f.db, f.contact).Destination, move.From)
}

func TestContactAddressAcceptanceCannotBorrowAnotherOperationsContactOrCandidate(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	move := addressMove(t, f, "move-bound", "bound.test:7445")
	other := messageContact(t, f.db, 2)
	for _, forged := range []store.ContactAddress{
		func() store.ContactAddress { v := move; v.Contact = other; return v }(),
		func() store.ContactAddress { v := move; v.To = "substitute.test:7445"; return v }(),
	} {
		_, err := f.db.ChangeContactAddress(ctx, forged)
		if !errors.Is(err, store.ErrAddressOpConflict) {
			t.Fatalf("borrowed operation returned %v, want conflict", err)
		}
	}
	pairEqual(t, contactGet(t, f.db, f.contact).Destination, move.From)
	_, err := f.db.ChangeContactAddress(ctx, move)
	pairOK(t, err)
	pairEqual(t, contactGet(t, f.db, f.contact).Destination, move.To)
	pairEqual(t, contactGet(t, f.db, other).Destination != move.To, true)
}

func TestContactAddressAcceptanceRestartSettlesOriginalAuditAndKeepsAcceptedState(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	move := addressMove(t, f, "move-recover", "recover.test:7445")
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	_, err := f.db.ChangeContactAddress(ctx, move)
	pairOK(t, err)
	pairOK(t, f.db.RefuseContactAddress(ctx, move.OpID, "late failure"))
	outcome, known, err := f.db.BeginContactAddress(ctx, store.AddressOp{ID: move.OpID, Contact: f.contact, Candidate: move.To, At: time.Now()})
	pairOK(t, err)
	pairEqual(t, known, true)
	pairEqual(t, outcome.State, store.AddressAccepted)
	pairEqual(t, contactGet(t, f.db, f.contact).Destination, move.To)
	var count int
	pairOK(t, f.raw.QueryRow(`SELECT count(*) FROM operator_audit WHERE action='contact address' AND message_id=? AND result='accepted'`, move.OpID).Scan(&count))
	pairEqual(t, count, 1)
	pairOK(t, f.raw.QueryRow(`SELECT count(*) FROM operator_audit WHERE action='contact address' AND message_id=? AND result='pending'`, move.OpID).Scan(&count))
	pairEqual(t, count, 0)
}

func TestContactAddressAcceptanceAuditAtomicityAndOperationBinding(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	move := addressMove(t, f, "move-audit", "new.test:443")
	_, err := f.raw.Exec(`CREATE TRIGGER deny_address_audit BEFORE UPDATE ON operator_audit
WHEN NEW.action='contact address' AND NEW.result='accepted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	pairOK(t, err)
	_, err = f.db.ChangeContactAddress(ctx, move)
	if err == nil {
		t.Fatal("address moved without accepted audit")
	}
	pairEqual(t, contactGet(t, f.db, f.contact).Destination, move.From)
	_, err = f.raw.Exec(`DROP TRIGGER deny_address_audit`)
	pairOK(t, err)
	_, err = f.db.ChangeContactAddress(ctx, move)
	pairOK(t, err)
	outcome, fresh, err := f.db.BeginContactAddress(ctx, store.AddressOp{ID: move.OpID, Contact: f.contact, Candidate: move.To, At: time.Now()})
	pairOK(t, err)
	pairEqual(t, fresh, true)
	pairEqual(t, outcome.State, store.AddressAccepted)
	_, _, err = f.db.BeginContactAddress(ctx, store.AddressOp{ID: move.OpID, Contact: f.contact, Candidate: "other.test:443", At: time.Now()})
	if !errors.Is(err, store.ErrAddressOpConflict) {
		t.Fatalf("changed replay returned %v, want operation conflict", err)
	}
	_, err = f.db.ChangeContactAddress(ctx, move)
	if err == nil {
		t.Fatal("accepted operation committed twice")
	}
	pairEqual(t, contactGet(t, f.db, f.contact).Destination, move.To)
	var count int
	pairOK(t, f.raw.QueryRow(`SELECT count(*) FROM operator_audit WHERE action='contact address' AND result='accepted'`).Scan(&count))
	pairEqual(t, count, 1)
}
