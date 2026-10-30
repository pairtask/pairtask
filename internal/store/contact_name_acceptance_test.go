package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/store"
)

func TestContactNameAcceptancePersistsAndClearsWithoutChangingIdentity(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	before := contactGet(t, f.db, f.contact)
	_, err := f.send(t, contactSendInput{"history", "keep this message"})
	pairOK(t, err)
	old, err := f.db.SetContactLocalName(ctx, store.ContactName{Contact: f.contact, Name: "Home Mac", AuditID: f.audit(t, contactAuditInput{"name", "rename"})})
	pairOK(t, err)
	pairEqual(t, old, "")
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	got := contactGet(t, f.db, f.contact)
	pairEqual(t, got.LocalName, "Home Mac")
	pairEqual(t, got.Name, before.Name)
	pairEqual(t, got.IdentityKey, before.IdentityKey)
	pairEqual(t, got.Destination, before.Destination)
	pairEqual(t, got.EstablishedAt.Equal(before.EstablishedAt), true)
	pairEqual(t, len(messagePage(t, f.db.PeerMessages(), f.contact)), 1)
	old, err = f.db.SetContactLocalName(ctx, store.ContactName{Contact: f.contact, Name: "", AuditID: f.audit(t, contactAuditInput{"name", "clear"})})
	pairOK(t, err)
	pairEqual(t, old, "Home Mac")
	pairEqual(t, contactGet(t, f.db, f.contact).LocalName, "")
	contactsCount(t, f.db, 1)
}

func TestContactNameAcceptanceAuditAndNameChangeAreAtomic(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	_, err := f.raw.Exec(`CREATE TRIGGER deny_name_audit BEFORE UPDATE ON operator_audit
WHEN NEW.action='contact name' AND NEW.result='accepted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	pairOK(t, err)
	r := store.ContactName{Contact: f.contact, Name: "Office", AuditID: f.audit(t, contactAuditInput{"name", "rename"})}
	_, err = f.db.SetContactLocalName(ctx, r)
	if err == nil {
		t.Fatal("rename succeeded without its audit")
	}
	pairEqual(t, contactGet(t, f.db, f.contact).LocalName, "")
	_, err = f.raw.Exec(`DROP TRIGGER deny_name_audit`)
	pairOK(t, err)
	_, err = f.db.SetContactLocalName(ctx, r)
	pairOK(t, err)
	_, err = f.db.SetContactLocalName(ctx, r)
	if err == nil {
		t.Fatal("already finished audit intent was reused")
	}
	pairEqual(t, contactGet(t, f.db, f.contact).LocalName, r.Name)
	var count int
	pairOK(t, f.raw.QueryRow(`SELECT count(*) FROM operator_audit WHERE action='contact name'`).Scan(&count))
	pairEqual(t, count, 1)
	var scope, op, detail string
	pairOK(t, f.raw.QueryRow(`SELECT scope,message_id,detail FROM operator_audit WHERE action='contact name'`).Scan(&scope, &op, &detail))
	pairEqual(t, scope, f.contact)
	pairEqual(t, op, "rename")
	pairEqual(t, detail, `"" -> "Office"`)
}

func TestContactNameAcceptanceInvalidOrUnknownCannotChangeContact(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	for _, name := range []string{strings.Repeat("x", 65), "forged\nline", "escape\x1b"} {
		_, err := f.db.SetContactLocalName(ctx, store.ContactName{Contact: f.contact, Name: name, AuditID: f.audit(t, contactAuditInput{"name", "bad-name"})})
		if err == nil {
			t.Fatalf("invalid name accepted: %q", name)
		}
	}
	_, err := f.db.SetContactLocalName(ctx, store.ContactName{Contact: "unknown", Name: "Office", AuditID: f.audit(t, contactAuditInput{"name", "unknown"})})
	pairEqual(t, errors.Is(err, store.ErrNoContact), true)
	pairEqual(t, contactGet(t, f.db, f.contact).LocalName, "")
	contactsCount(t, f.db, 1)
}

func TestContactNameAcceptanceMigratesContactsWithoutAliases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	pairOK(t, err)
	_, err = raw.Exec(`CREATE TABLE peer_contacts (
identity_key TEXT PRIMARY KEY, name TEXT NOT NULL, destination TEXT NOT NULL,
established_at TEXT NOT NULL, joined INTEGER NOT NULL, invited INTEGER NOT NULL)`)
	pairOK(t, err)
	_, err = raw.Exec(`INSERT INTO peer_contacts VALUES(?,?,?,?,?,?)`, joinKey(2), "Remote name", "host:443", "2026-10-30T12:00:00Z", 1, 0)
	pairOK(t, err)
	pairOK(t, raw.Close())
	db := pairSQLite(t, path)
	c := contactGet(t, db, joinKey(2))
	pairEqual(t, c.LocalName, "")
	pairEqual(t, c.Name, "Remote name")
	id, err := db.RecordOperatorAudit(context.Background(), store.OperatorAudit{Action: "contact name", MessageID: "migrated-name", Scope: c.IdentityKey, Result: store.AuditPending})
	pairOK(t, err)
	_, err = db.SetContactLocalName(context.Background(), store.ContactName{Contact: c.IdentityKey, Name: "My host", AuditID: id})
	pairOK(t, err)
	pairEqual(t, contactGet(t, db, c.IdentityKey).LocalName, "My host")
}
