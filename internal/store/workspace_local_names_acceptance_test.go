package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/store"
)

func setWorkspaceAlias(t *testing.T, db *store.Store, key, name string) {
	t.Helper()
	ctx := context.Background()
	audit, err := db.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "contact name", Scope: key, Result: store.AuditPending})
	pairOK(t, err)
	_, err = db.SetContactLocalName(ctx, store.ContactName{Contact: key, Name: name, AuditID: audit})
	pairOK(t, err)
}

func workspaceResolves(t *testing.T, db *store.Store, ref, want string) {
	t.Helper()
	key, err := db.ResolveWorkspace(context.Background(), ref)
	pairOK(t, err)
	pairEqual(t, key, want)
}

func workspaceRejects(t *testing.T, db *store.Store, ref string, want error) {
	t.Helper()
	key, err := db.ResolveWorkspace(context.Background(), ref)
	if !errors.Is(err, want) {
		t.Fatalf("ResolveWorkspace(%q) = (%q, %v), want %v", ref, key, err, want)
	}
	pairEqual(t, key, "")
}

func TestWorkspaceLocalNamesStoreAcceptanceAliasRenameAndClear(t *testing.T) {
	f := workspaceStore(t)
	setWorkspaceAlias(t, f.db, f.key, "Local workstation")
	workspaceResolves(t, f.db, "Local workstation", f.key)
	workspaceResolves(t, f.db, "Alice", f.key)
	workspaceResolves(t, f.db, f.key, f.key)
	setWorkspaceAlias(t, f.db, f.key, "Renamed workstation")
	workspaceRejects(t, f.db, "Local workstation", store.ErrNoWorkspace)
	workspaceResolves(t, f.db, "Renamed workstation", f.key)
	workspaceResolves(t, f.db, "Alice", f.key)
	setWorkspaceAlias(t, f.db, f.key, "")
	workspaceRejects(t, f.db, "Renamed workstation", store.ErrNoWorkspace)
	workspaceResolves(t, f.db, "Alice", f.key)
	workspaceResolves(t, f.db, f.key, f.key)
	pairEqual(t, contactGet(t, f.db, f.key).Name, "Alice")
}

func TestWorkspaceLocalNamesStoreAcceptanceCrossContactNamesAreAmbiguous(t *testing.T) {
	f := workspaceStore(t)
	other := messageContact(t, f.db, 2)
	_, err := f.raw.Exec("UPDATE peer_contacts SET name='Second peer' WHERE identity_key=?", other)
	pairOK(t, err)
	setWorkspaceAlias(t, f.db, f.key, "Second peer")
	workspaceRejects(t, f.db, "Second peer", store.ErrAmbiguousWorkspace)
	// A contact matching by both fields still contributes only one candidate.
	setWorkspaceAlias(t, f.db, other, "Second peer")
	workspaceRejects(t, f.db, "Second peer", store.ErrAmbiguousWorkspace)
	setWorkspaceAlias(t, f.db, f.key, "Shared alias")
	setWorkspaceAlias(t, f.db, other, "Shared alias")
	workspaceRejects(t, f.db, "Shared alias", store.ErrAmbiguousWorkspace)
	workspaceResolves(t, f.db, f.key, f.key)
	workspaceResolves(t, f.db, other, other)
}

func TestWorkspaceLocalNamesStoreAcceptanceSameContactNamesDeduplicate(t *testing.T) {
	f := workspaceStore(t)
	setWorkspaceAlias(t, f.db, f.key, "Alice")
	workspaceResolves(t, f.db, "Alice", f.key)
}

func TestWorkspaceLocalNamesStoreAcceptanceExactKeyWinsOverNameCollision(t *testing.T) {
	f := workspaceStore(t)
	other := messageContact(t, f.db, 2)
	// Seed legacy names directly: current operator names cannot be this long.
	_, err := f.raw.Exec("UPDATE peer_contacts SET name=?,local_name=? WHERE identity_key=?", f.key, f.key, other)
	pairOK(t, err)
	_, err = f.raw.Exec("UPDATE peer_contacts SET name=?,local_name=? WHERE identity_key=?", other, other, f.key)
	pairOK(t, err)
	workspaceResolves(t, f.db, f.key, f.key)
	workspaceResolves(t, f.db, other, other)
}

func TestWorkspaceLocalNamesStoreAcceptanceEmptyAndPrefixesAreRejected(t *testing.T) {
	f := workspaceStore(t)
	setWorkspaceAlias(t, f.db, f.key, "Local workstation")
	for _, ref := range []string{"", f.key[:20], "Local work", "Ali", "unknown"} {
		workspaceRejects(t, f.db, ref, store.ErrNoWorkspace)
	}
	// An empty legacy self-name must not turn the empty ref into a match.
	_, err := f.raw.Exec("UPDATE peer_contacts SET name='' WHERE identity_key=?", f.key)
	pairOK(t, err)
	workspaceRejects(t, f.db, "", store.ErrNoWorkspace)
}

func TestWorkspaceLocalNamesStoreAcceptanceUnconfirmedNamesAreExcluded(t *testing.T) {
	f := workspaceStore(t)
	r := contactReady(t, f.db, "unconfirmed-workspace")
	destinationAccept(t, f.db, r, joinAt) // contact exists; decision is not confirmed
	c := contactGet(t, f.db, r.Peer)
	setWorkspaceAlias(t, f.db, c.IdentityKey, "Local unconfirmed")
	for _, ref := range []string{c.IdentityKey, c.Name, "Local unconfirmed"} {
		workspaceRejects(t, f.db, ref, store.ErrNoWorkspace)
	}
	setWorkspaceAlias(t, f.db, c.IdentityKey, "Alice")
	workspaceResolves(t, f.db, "Alice", f.key)
}
