package store_test

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func epochUniqueKeys(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_index_list(?) WHERE "unique"=1`, table)
	pairOK(t, err)
	var names []string
	for rows.Next() {
		var name string
		pairOK(t, rows.Scan(&name))
		names = append(names, name)
	}
	pairOK(t, rows.Err())
	pairOK(t, rows.Close())
	var keys []string
	for _, name := range names {
		keys = append(keys, strings.Join(epochIndexColumns(t, db, name), ","))
	}
	sort.Strings(keys)
	return keys
}

func epochExpectedKeys() map[string][]string {
	return map[string][]string{
		"peer_messages":          {"contact,pairing_epoch,direction,message_id", "contact,pairing_epoch,direction,n"},
		"peer_message_sequences": {"contact,pairing_epoch"},
		"peer_message_floors":    {"contact,pairing_epoch"},
		"peer_ack_floors":        {"contact,pairing_epoch"},
		"peer_agent_reads":       {"contact,pairing_epoch,message_id,agent"},
		"peer_agent_acks":        {"contact,pairing_epoch,message_id,agent"},
		"remote_imports":         {"contact,pairing_epoch,remote_id", "local_id"},
		"peer_grants":            {"contact,pairing_epoch,agent"},
		"peer_review_grants":     {"contact,pairing_epoch,agent"},
		"peer_directory":         {"contact,pairing_epoch,agent"},
		"review_asks_out":        {"contact,pairing_epoch,ask_id"},
		"review_outcome_seq":     {"contact,pairing_epoch,ask_id"},
		"review_outcomes_out":    {"contact,pairing_epoch,ask_id"},
		"consent_requests":       {"contact,pairing_epoch,message_id", "id"},
		"consent_held_out":       {"contact,pairing_epoch,message_id"},
		"consent_decisions":      {"contact,pairing_epoch,agent,operation"},
		"consent_sessions":       {"contact,pairing_epoch,agent,operation"},
	}
}

func TestEpochMigrationAcceptanceKeysMatchOnFreshAndReleasedDatabases(t *testing.T) {
	for _, released := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "released"}[released], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fresh.db")
			if released {
				path, _, _ = epochReleased(t)
			}
			epochOpen(t, path)
			raw := epochRaw(t, path)
			for table, want := range epochExpectedKeys() {
				sort.Strings(want)
				if got := epochUniqueKeys(t, raw, table); !reflect.DeepEqual(got, want) {
					t.Errorf("%s unique keys %v, want %v", table, got, want)
				}
			}
		})
	}
}
