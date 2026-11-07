//go:build linux

package server

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/store"
)

func (f *controlServerFixture) remoteInspection(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", strings.TrimSuffix(f.remote.PeerDir(), ".peer"))
	operatorNoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}
func (f *controlServerFixture) unpair(t *testing.T) {
	t.Helper()
	f.unpairStore(t, f.f.st, f.id.Fingerprint())
}
func (f *controlServerFixture) remoteUnpair(t *testing.T) {
	t.Helper()
	f.unpairStore(t, f.remote, f.route.pin)
}
func (f *controlServerFixture) unpairStore(t *testing.T, db *store.Store, key string) {
	t.Helper()
	audit, err := db.RecordOperatorAudit(context.Background(), store.OperatorAudit{Action: "contact unpair", MessageID: "control-local-unpair", Scope: key, Result: store.AuditPending})
	operatorNoError(t, err)
	_, _, err = db.UnpairContact(context.Background(), store.ContactUnpair{OperationID: "control-local-unpair", Contact: key, Confirm: key, SeenEpoch: 1, AuditID: audit, At: time.Now()})
	operatorNoError(t, err)
}
