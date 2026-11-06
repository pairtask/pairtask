package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/store"
)

var epochRebuildOrder = []string{
	"peer_messages", "peer_message_sequences", "peer_message_floors", "peer_ack_floors",
	"peer_agent_reads", "peer_agent_acks", "remote_imports", "peer_grants", "peer_review_grants",
	"peer_directory", "review_asks_out", "review_outcome_seq", "review_outcomes_out",
	"consent_requests", "consent_held_out", "consent_decisions", "consent_sessions",
}

func epochExpectedSteps(released bool) []string {
	steps := []string{"begin", "shape"}
	if released {
		for _, table := range epochRebuildOrder {
			steps = append(steps, "rebuild:"+table)
		}
	}
	return append(steps, "indexes", "columns", "ledger", "foreign_key_check", "marker", "commit")
}

func epochOpen(t *testing.T, path string) {
	t.Helper()
	db, err := store.Open(path)
	pairOK(t, err)
	pairOK(t, db.Close())
}

func TestEpochMigrationAcceptanceReleasedRowsAndHighWaterSurvive(t *testing.T) {
	path, raw, manifest := epochReleased(t)
	before := epochSnapshot(t, raw, manifest)
	epochFixtureCoverage(t, manifest)
	epochOpen(t, path)
	epochAssertReleasedRows(t, raw, before)
	for _, name := range epochRebuildOrder {
		pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM "+epochQuote(name)+" WHERE pairing_epoch != 1"), 0)
	}
	pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM peer_contacts WHERE pairing_id IS NOT NULL"), 0)
	for name, sequence := range manifest.Sequences {
		epochSequence(t, raw, name, sequence)
	}
	epochNoForeignKeyErrors(t, raw)
	pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM schema_steps WHERE name=?", store.EpochSchemaStep), 1)
	objects := epochSchemaObjects(t, raw)
	epochOpen(t, path)
	if !reflect.DeepEqual(objects, epochSchemaObjects(t, raw)) {
		t.Fatal("reopen changed the migrated schema")
	}
	for name, sequence := range manifest.Sequences {
		epochSequence(t, raw, name, sequence)
	}
}

func TestEpochMigrationAcceptanceOrderedStepsFreshReleasedAndReopen(t *testing.T) {
	if !reflect.DeepEqual(store.MigrationRebuildTables, epochRebuildOrder) || !reflect.DeepEqual(store.MigrationSteps(), epochExpectedSteps(true)) {
		t.Fatal("migration seam does not describe the frozen transition order")
	}
	for _, released := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "released"}[released], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fresh.db")
			if released {
				path, _, _ = epochReleased(t)
			}
			var seen []string
			restore := store.SetMigrationHook(func(step string) error { seen = append(seen, step); return nil })
			defer restore()
			epochOpen(t, path)
			if !reflect.DeepEqual(seen, epochExpectedSteps(released)) {
				t.Fatalf("steps %v", seen)
			}
			seen = nil
			epochOpen(t, path)
			if len(seen) != 0 {
				t.Fatalf("marked reopen ran steps %v", seen)
			}
		})
	}
}

func TestEpochMigrationAcceptanceEveryStepRollsBackAndRetries(t *testing.T) {
	for _, failAt := range epochExpectedSteps(true) {
		t.Run(epochLabel(failAt), func(t *testing.T) {
			path, raw, manifest := epochReleased(t)
			before := epochSnapshot(t, raw, manifest)
			objects := epochSchemaObjects(t, raw)
			sentinel := errors.New("injected epoch migration failure")
			restore := store.SetMigrationHook(func(step string) error {
				if step == failAt {
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
				t.Fatalf("%s: expected wrapped hook error, got %v", failAt, err)
			}
			if got := epochSnapshot(t, raw, manifest); !reflect.DeepEqual(got, before) {
				t.Fatal("failed transition changed released schema or data")
			}
			if !reflect.DeepEqual(objects, epochSchemaObjects(t, raw)) {
				t.Fatal("failed transition changed schema objects")
			}
			pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM schema_steps WHERE name=?", store.EpochSchemaStep), 0)
			for name, sequence := range manifest.Sequences {
				epochSequence(t, raw, name, sequence)
			}
			restore()
			epochOpen(t, path)
			epochNoForeignKeyErrors(t, raw)
			pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM schema_steps WHERE name=?", store.EpochSchemaStep), 1)
		})
	}
}

func TestEpochMigrationAcceptanceUnknownSchemaAbortsWithoutRebuild(t *testing.T) {
	changes := map[string]string{
		"column":  "ALTER TABLE peer_messages ADD COLUMN private_extension TEXT",
		"index":   "CREATE INDEX private_extension ON peer_messages(text_hash)",
		"trigger": "CREATE TRIGGER private_extension AFTER INSERT ON peer_messages BEGIN SELECT 1; END",
		"partial": "ALTER TABLE peer_grants ADD COLUMN pairing_epoch INTEGER NOT NULL DEFAULT 1",
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			path, raw, manifest := epochReleased(t)
			_, err := raw.Exec(change)
			pairOK(t, err)
			before := epochSnapshot(t, raw, manifest)
			objects := epochSchemaObjects(t, raw)
			db, err := store.Open(path)
			if db != nil {
				_ = db.Close()
			}
			if err == nil {
				t.Fatal("unsupported released schema migrated")
			}
			if !reflect.DeepEqual(before, epochSnapshot(t, raw, manifest)) {
				t.Fatal("rejected shape changed released contents")
			}
			if !reflect.DeepEqual(objects, epochSchemaObjects(t, raw)) {
				t.Fatal("rejected shape changed schema objects")
			}
		})
	}
}

func TestEpochMigrationAcceptanceIndexesAndFreshDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	epochOpen(t, path)
	raw := epochRaw(t, path)
	indexes := map[string][]string{
		"peer_messages_state":    {"contact", "pairing_epoch", "direction", "state"},
		"consent_requests_state": {"state", "contact", "pairing_epoch"},
		"remote_imports_pending": {"state", "staged_at"},
	}
	for name, want := range indexes {
		epochAssertIndex(t, raw, name, want)
	}
	for _, name := range epochRebuildOrder {
		epochAssertDefault(t, raw, name)
	}
	pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM pairing_marks WHERE id=1 AND mark=0"), 1)
	epochNoForeignKeyErrors(t, raw)
}

func epochFixtureCoverage(t *testing.T, manifest epochFixtureManifest) {
	t.Helper()
	for _, name := range epochRebuildOrder {
		epochFixturePopulated(t, manifest, name)
	}
	for name, sequence := range manifest.Sequences {
		if sequence <= manifest.MaxCursor[name] {
			t.Fatalf("fixture lacks retention high water for %s", name)
		}
	}
}

func epochFixturePopulated(t *testing.T, manifest epochFixtureManifest, name string) {
	t.Helper()
	if manifest.Tables[name] == 0 {
		t.Fatalf("released fixture does not exercise %s", name)
	}
}

func epochAssertReleasedRows(t *testing.T, raw *sql.DB, before map[string]epochTableSnapshot) {
	t.Helper()
	for name, snapshot := range before {
		if name == "consent_owner" || name == "consent_sessions" {
			// Opening a new broker incarnation deliberately revokes live session
			// authority (#293). Failed migrations above must still roll it back.
			pairEqual(t, epochCount(t, raw, "SELECT count(*) FROM "+epochQuote(name)), 0)
			continue
		}
		if got := epochRows(t, raw, name, snapshot.Columns); !reflect.DeepEqual(got, snapshot.Rows) {
			t.Errorf("migration changed released %s rows", name)
		}
	}
}

func epochAssertDefault(t *testing.T, raw *sql.DB, name string) {
	t.Helper()
	var notNull int
	var defaultValue string
	pairOK(t, raw.QueryRow(`SELECT "notnull",dflt_value FROM pragma_table_info(?) WHERE name='pairing_epoch'`, name).Scan(&notNull, &defaultValue))
	if notNull != 1 || strings.Trim(defaultValue, "()'") != "1" {
		t.Errorf("%s epoch default %q notnull=%d", name, defaultValue, notNull)
	}
}

func epochAssertIndex(t *testing.T, raw *sql.DB, name string, want []string) {
	t.Helper()
	if got := epochIndexColumns(t, raw, name); !reflect.DeepEqual(got, want) {
		t.Errorf("%s columns %v, want %v", name, got, want)
	}
}
