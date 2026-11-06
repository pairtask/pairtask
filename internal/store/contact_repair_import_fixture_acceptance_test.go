package store_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func namespaceFixture(t *testing.T) workspaceStoreFixture {
	t.Helper()
	f := workspaceStore(t)
	routingRegister(t, f.db, "recipient")
	namespaceShare(t, f, "recipient")
	return f
}

func namespaceShare(t *testing.T, f workspaceStoreFixture, agents ...string) {
	t.Helper()
	_, err := f.db.PeerMessages().Share(context.Background(), f.key, agents, messageAt)
	pairOK(t, err)
}

// Install only the store state for a second confirmed pairing. The wire
// exchange is deliberately outside these import-namespace tests.
func namespaceRepair(t *testing.T, f workspaceStoreFixture) {
	t.Helper()
	op := &operatorContactFixture{db: f.db, raw: f.raw, contact: f.key}
	_, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, op, "namespace-unpair"))
	pairOK(t, err)
	_, err = f.raw.Exec(`UPDATE peer_contacts SET pairing_epoch=2,pairing_id=?,unpaired_at='',generation=generation+1 WHERE identity_key=?`, make([]byte, 32), f.key)
	pairOK(t, err)
	_, err = f.raw.Exec(`UPDATE peer_pairing_requests SET state='accepted',delivered_at='2026-11-06T00:00:00Z' WHERE identity_key=?`, f.key)
	pairOK(t, err)
}

func namespaceMessage(id, text string) messaging.Message {
	m := messageWire(id, 1)
	m.From, m.To, m.Text = "remote-sender", []string{"recipient"}, text
	return m
}

func namespaceAccept(t *testing.T, f workspaceStoreFixture, m messaging.Message) {
	t.Helper()
	a, err := f.db.PeerMessages().Accept(context.Background(), f.key, m, messageAt)
	pairOK(t, err)
	if !a.Stored {
		t.Fatalf("explicitly granted import was refused: %+v", a)
	}
}

func namespacePending(t *testing.T, f workspaceStoreFixture, id string) store.RemoteImport {
	t.Helper()
	rows, err := f.db.PendingImports(context.Background(), 100)
	pairOK(t, err)
	var matches []store.RemoteImport
	for _, r := range rows {
		if r.Contact == f.key && r.RemoteID == id {
			matches = append(matches, r)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("pending source for %s: got %d rows, want 1", id, len(matches))
	}
	return matches[0]
}

func namespaceEpochRows(t *testing.T, f workspaceStoreFixture, table string, epoch int) []string {
	t.Helper()
	var fields []string
	for _, c := range epochColumns(t, f.raw, table) {
		fields = append(fields, "quote("+epochQuote(c)+")")
	}
	query := "SELECT " + strings.Join(fields, " || ',' || ") + " FROM " + epochQuote(table) + " WHERE pairing_epoch = ?"
	rows, err := f.raw.Query(query, epoch)
	pairOK(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var row string
		pairOK(t, rows.Scan(&row))
		out = append(out, row)
	}
	pairOK(t, rows.Err())
	sort.Strings(out)
	return out
}
