package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func roomStoreCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func roomScalar[T comparable](t *testing.T, st *Store, query string, want T) {
	t.Helper()
	var got T
	roomStoreCheck(t, st.db.QueryRow(query).Scan(&got))
	assertStatusEqual(t, query, got, want)
}

func roomLegacyDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pre-rooms.db")
	db, err := sql.Open("sqlite", path)
	roomStoreCheck(t, err)
	defer db.Close()
	_, err = db.Exec(schema + durableSchema + operatorSchema + agentStatusSchema + claimsSchema)
	roomStoreCheck(t, err)
	_, err = db.Exec(`
INSERT INTO agents(agent_id,session_id,connected_at,disconnected_at) VALUES('session-only','old-session','2026-01-01','2026-01-02');
INSERT INTO durable_agents(agent_id) VALUES('durable-only'),('operator');
INSERT INTO messages(seq,ts,sender,recipient,payload) VALUES(42,'2026-01-01','operator','inbox-only','{"text":"retained"}');
INSERT INTO message_metadata(message_id,seq,sender_session,in_reply_to,ack_requested) VALUES('legacy-parent',42,'operator-session','',1);
INSERT INTO durable_messages VALUES('legacy-parent',100,1124);
INSERT INTO durable_inbox(message_id,agent_id,session_id) VALUES('legacy-parent','inbox-only','old-inbox-session');
INSERT INTO agent_status(agent_id,session_id,state,task,step,waiting_on,ref,declared_at) VALUES('status-only','old-status-session','blocked','keep-task','','','','2026-01-01');
INSERT INTO claims(operator_message_id,task,role,agent_id,topic,state,claimed_at) VALUES('legacy-parent','review','owner','claim-only','keep-topic','active','2026-01-01');`)
	roomStoreCheck(t, err)
	return path
}

func TestRoomsMigrationAcceptancePreservesLegacyDataWithoutBackfill(t *testing.T) {
	path := roomLegacyDatabase(t)
	for range 2 {
		st, err := Open(path)
		roomStoreCheck(t, err)
		roomScalar(t, st, "SELECT COUNT(*) FROM rooms", 0)
		roomScalar(t, st, "SELECT COUNT(*) FROM room_members", 0)
		roomScalar(t, st, "SELECT COUNT(*) FROM message_metadata WHERE message_id='legacy-parent' AND room IS NULL", 1)
		roomScalar(t, st, "SELECT seq FROM message_metadata WHERE message_id='legacy-parent'", 42)
		roomScalar(t, st, "SELECT COUNT(*) FROM durable_inbox WHERE acknowledged_at IS NULL", 1)
		roomScalar(t, st, "SELECT state FROM claims WHERE task='review'", "active")
		roomScalar(t, st, "SELECT task FROM agent_status WHERE agent_id='status-only'", "keep-task")
		roomStoreCheck(t, st.Close())
	}
}

func TestRoomsMigrationAcceptanceMultipleMembershipsSurviveRestart(t *testing.T) {
	path := roomLegacyDatabase(t)
	st, err := Open(path)
	roomStoreCheck(t, err)
	_, err = st.db.Exec(`INSERT INTO rooms(id,created_at) VALUES('alpha','2026-10-25'),('beta','2026-10-25'),('empty','2026-10-25');
INSERT INTO room_members(room_id,agent_id,joined_at) VALUES('alpha','worker','2026-10-25'),('beta','worker','2026-10-25');
INSERT INTO messages(seq,ts,sender,recipient,payload) VALUES(43,'2026-10-25','worker','*','{}');
INSERT INTO message_metadata(message_id,seq,sender_session,in_reply_to,ack_requested,room) VALUES('room-message',43,'session','',1,'alpha');`)
	roomStoreCheck(t, err)
	roomStoreCheck(t, st.Close())
	st, err = Open(path)
	roomStoreCheck(t, err)
	defer st.Close()
	roomScalar(t, st, "SELECT COUNT(*) FROM room_members WHERE agent_id='worker'", 2)
	roomScalar(t, st, "SELECT COUNT(*) FROM rooms", 3)
	roomScalar(t, st, "SELECT room FROM message_metadata WHERE message_id='room-message'", "alpha")
	roomScalar(t, st, "SELECT COUNT(*) FROM message_metadata WHERE message_id='legacy-parent' AND room IS NULL", 1)
}
