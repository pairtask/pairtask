package store_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/store"
)

func epochPreConsent(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dump, err := os.ReadFile("testdata/released-836.1.sql")
	pairOK(t, err)
	data, err := os.ReadFile("testdata/released-836.1.json")
	pairOK(t, err)
	var provenance struct {
		Commit string `json:"commit"`
		SQLSHA string `json:"sql_sha256"`
	}
	pairOK(t, json.Unmarshal(data, &provenance))
	pairEqual(t, provenance.Commit, "9f12564924f35d0b2a9025ae57416fbd9feffd44")
	pairEqual(t, fmt.Sprintf("%x", sha256.Sum256(dump)), provenance.SQLSHA)
	path := filepath.Join(t.TempDir(), "released-836.db")
	raw := epochRaw(t, path)
	_, err = raw.Exec(string(dump))
	pairOK(t, err)
	pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name LIKE 'consent_%'"), 0)
	_, err = raw.Exec("INSERT INTO peer_message_floors(contact,floor) VALUES ('migration-contact',42)")
	pairOK(t, err)
	return path, raw
}

func TestEpochMigrationPreConsentReleasedDatabase(t *testing.T) {
	path, raw := epochPreConsent(t)
	var rebuilt []string
	restore := store.SetMigrationHook(func(step string) error {
		if strings.HasPrefix(step, "rebuild:") {
			rebuilt = append(rebuilt, strings.TrimPrefix(step, "rebuild:"))
		}
		return nil
	})
	defer restore()
	epochOpen(t, path)
	want := epochPreConsentRebuilds()
	if !reflect.DeepEqual(rebuilt, want) {
		t.Fatalf("pre-consent rebuilds %v, want %v", rebuilt, want)
	}
	pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM peer_message_floors WHERE contact='migration-contact' AND floor=42 AND pairing_epoch=1"), 1)
	epochPreConsentAssertKeys(t, raw)
	epochNoForeignKeyErrors(t, raw)
	restore()
	epochOpen(t, path)
}

func epochPreConsentRebuilds() []string {
	var want []string
	for _, table := range epochRebuildOrder {
		if !strings.HasPrefix(table, "consent_") {
			want = append(want, table)
		}
	}
	return want
}

func epochPreConsentAssertKeys(t *testing.T, raw *sql.DB) {
	t.Helper()
	for table, keys := range epochExpectedKeys() {
		got := epochUniqueKeys(t, raw, table)
		sort.Strings(keys)
		if !reflect.DeepEqual(got, keys) {
			t.Errorf("%s keys %v, want %v", table, got, keys)
		}
	}
}

func TestEpochMigrationPreConsentFailureCanReopen(t *testing.T) {
	path, raw := epochPreConsent(t)
	sentinel := errors.New("pre-consent migration failure")
	restore := store.SetMigrationHook(func(step string) error {
		if step == "marker" {
			return sentinel
		}
		return nil
	})
	defer restore()
	db, err := store.Open(path)
	if db != nil {
		_ = db.Close()
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected injected transition failure, got %v", err)
	}
	pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM pragma_table_info('peer_message_floors') WHERE name='pairing_epoch'"), 0)
	pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM peer_message_floors WHERE contact='migration-contact' AND floor=42"), 1)
	restore()
	// Open may have created previously absent tables before the failed
	// transition. Those tables must not make the retry reject its own work.
	epochOpen(t, path)
	pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM schema_steps WHERE name=?", store.EpochSchemaStep), 1)
}

func TestEpochMigrationPreConsentEarlierAckCallbackHasItsTable(t *testing.T) {
	path, raw := epochPreConsent(t)
	// Reconstruct the earlier ACK-less shape to exercise the retained
	// addedColumns callback, which seeds peer_ack_floors when acked_at is
	// first added. This is a deliberate fixture variation, not an untouched
	// database claimed to have been emitted by release 836.1.
	_, err := raw.Exec(`ALTER TABLE peer_messages DROP COLUMN acked_at;
ALTER TABLE peer_messages DROP COLUMN ack_expired_at;
DROP TABLE peer_ack_floors;`)
	pairOK(t, err)
	epochOpen(t, path)
	pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM pragma_table_info('peer_ack_floors') WHERE name='pairing_epoch'"), 1)
	epochNoForeignKeyErrors(t, raw)
}
