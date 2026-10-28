package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

type operatorContactFixture struct {
	db            *store.Store
	raw           *sql.DB
	contact, path string
}

func newOperatorContactFixture(t *testing.T) *operatorContactFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "operator-contact.db")
	db := pairSQLite(t, path)
	raw, err := sql.Open("sqlite", path)
	pairOK(t, err)
	t.Cleanup(func() { raw.Close() })
	return &operatorContactFixture{db, raw, messageContact(t, db, 1), path}
}
func (f *operatorContactFixture) audit(t *testing.T, op, id string) int64 {
	t.Helper()
	audit, err := f.db.RecordOperatorAudit(context.Background(), store.OperatorAudit{Action: "contact " + op, MessageID: id, Scope: f.contact, Result: store.AuditPending})
	pairOK(t, err)
	return audit
}
func (f *operatorContactFixture) send(t *testing.T, id, text string) (messaging.Row, error) {
	t.Helper()
	return f.db.OperatorContactSend(context.Background(), store.ContactSend{OperationID: id, Contact: f.contact, Text: text, AuditID: f.audit(t, "send", id), Now: messageAt})
}
func (f *operatorContactFixture) read(t *testing.T, id string, ids []string) (int, error) {
	t.Helper()
	return f.db.OperatorContactRead(context.Background(), store.ContactRead{OperationID: id, Contact: f.contact, IDs: ids, AuditID: f.audit(t, "read", id), Now: messageAt})
}

func TestOperatorContactAcceptanceSendReplaySurvivesRestart(t *testing.T) {
	f := newOperatorContactFixture(t)
	first, err := f.send(t, "send-op", "private text")
	pairOK(t, err)
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	again, err := f.db.OperatorContactSend(context.Background(), store.ContactSend{OperationID: "send-op", Contact: f.contact, Text: "private text", AuditID: f.audit(t, "send", "send-op"), Now: messageAt.Add(time.Hour)})
	pairOK(t, err)
	pairEqual(t, again.ID, first.ID)
	pairEqual(t, again.N, first.N)
	pairEqual(t, again.Cursor, first.Cursor)
	pairEqual(t, again.CreatedAt.Equal(first.CreatedAt), true)
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), f.contact)), 1)
	_, err = f.send(t, "send-op", "changed text")
	pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
}

func TestOperatorContactAcceptanceSendAuditFailureRollsBackEverything(t *testing.T) {
	f := newOperatorContactFixture(t)
	_, err := f.raw.Exec("CREATE TRIGGER deny_contact_audit BEFORE UPDATE ON operator_audit WHEN NEW.result='accepted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END")
	pairOK(t, err)
	row, err := f.send(t, "send-op", "text")
	if err == nil || row.ID != "" {
		t.Fatalf("send survived audit failure: %+v %v", row, err)
	}
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), f.contact)), 0)
	var count int
	pairOK(t, f.raw.QueryRow("SELECT COUNT(*) FROM operator_contact_requests").Scan(&count))
	pairEqual(t, count, 0)
	_, err = f.raw.Exec("DROP TRIGGER deny_contact_audit")
	pairOK(t, err)
	row, err = f.send(t, "send-op", "text")
	pairOK(t, err)
	pairEqual(t, row.N, uint64(1))
}

func TestOperatorContactAcceptanceReadAuditFailurePreservesUnread(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	_, err := f.db.PeerMessages().Accept(ctx, f.contact, messageWire("in", 1), messageAt)
	pairOK(t, err)
	_, err = f.raw.Exec("CREATE TRIGGER deny_contact_audit BEFORE UPDATE ON operator_audit WHEN NEW.result='accepted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END")
	pairOK(t, err)
	marked, err := f.read(t, "read-op", []string{"in"})
	if err == nil || marked != 0 {
		t.Fatalf("read survived audit failure: %d %v", marked, err)
	}
	pairEqual(t, messagePage(t, f.db.PeerMessages(), f.contact)[0].State, messaging.StateReceived)
	receipts, err := f.db.PeerMessages().PendingReads(ctx, f.contact, 16)
	pairOK(t, err)
	pairEqual(t, len(receipts), 0)
	_, err = f.raw.Exec("DROP TRIGGER deny_contact_audit")
	pairOK(t, err)
	marked, err = f.read(t, "read-op", []string{"in"})
	pairOK(t, err)
	pairEqual(t, marked, 1)
}

func TestOperatorContactAcceptanceReadSetCanonicalAndOperationScoped(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	for i, id := range []string{"one", "two"} {
		_, err := f.db.PeerMessages().Accept(ctx, f.contact, messageWire(id, uint64(i+1)), messageAt)
		pairOK(t, err)
	}
	marked, err := f.read(t, "read-op", []string{"two", "one", "two"})
	pairOK(t, err)
	pairEqual(t, marked, 2)
	marked, err = f.read(t, "read-op", []string{"one", "two"})
	pairOK(t, err)
	pairEqual(t, marked, 0)
	_, err = f.read(t, "read-op", []string{"one"})
	pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
	_, err = f.send(t, "read-op", "cannot repurpose operation")
	pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
	receipts, err := f.db.PeerMessages().PendingReads(ctx, f.contact, 16)
	pairOK(t, err)
	pairEqual(t, len(receipts), 2)
}

func TestOperatorContactAcceptanceCrossContactRetryConflictsWithoutChangingTransport(t *testing.T) {
	f := newOperatorContactFixture(t)
	other := messageContact(t, f.db, 2)
	_, err := f.send(t, "operation", "text")
	pairOK(t, err)
	original := f.contact
	f.contact = other
	_, err = f.send(t, "operation", "text")
	pairEqual(t, errors.Is(err, messaging.ErrConflict), true)
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), other)), 0)
	// The transport API still permits independent peers to use the same ID.
	_, err = f.db.PeerMessages().Queue(context.Background(), other, "operation", "text", messageAt)
	pairOK(t, err)
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), original)), 1)
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), other)), 1)
}
