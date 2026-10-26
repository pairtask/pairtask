package store

import (
	"database/sql"
	"encoding/json"
	"testing"
)

func TestMentionsMigrationAcceptanceLegacyRowsRemainUnaddressed(t *testing.T) {
	path := roomLegacyDatabase(t)
	for range 2 {
		s, err := Open(path)
		roomStoreCheck(t, err)
		var mentions sql.NullString
		roomStoreCheck(t, s.db.QueryRow("SELECT mentions FROM message_metadata WHERE message_id='legacy-parent'").Scan(&mentions))
		if mentions.Valid {
			t.Fatalf("migration invented legacy mentions: %q", mentions.String)
		}
		roomScalar(t, s, "SELECT COUNT(*) FROM durable_inbox WHERE message_id='legacy-parent'", 1)
		roomScalar(t, s, "SELECT task FROM agent_status WHERE agent_id='status-only'", "keep-task")
		roomStoreCheck(t, s.Close())
	}
}

func TestMentionsMigrationAcceptanceExistingMetadataSurvivesReopen(t *testing.T) {
	path := roomLegacyDatabase(t)
	s, err := Open(path)
	roomStoreCheck(t, err)
	_, err = s.db.Exec(`UPDATE message_metadata SET mentions='["worker with spaces","worker-two"]' WHERE message_id='legacy-parent'`)
	roomStoreCheck(t, err)
	roomStoreCheck(t, s.Close())
	s, err = Open(path)
	roomStoreCheck(t, err)
	defer s.Close()
	var raw string
	roomStoreCheck(t, s.db.QueryRow("SELECT mentions FROM message_metadata WHERE message_id='legacy-parent'").Scan(&raw))
	var ids []string
	roomStoreCheck(t, json.Unmarshal([]byte(raw), &ids))
	if len(ids) != 2 || ids[0] != "worker with spaces" || ids[1] != "worker-two" {
		t.Fatalf("metadata changed: %s", raw)
	}
}
