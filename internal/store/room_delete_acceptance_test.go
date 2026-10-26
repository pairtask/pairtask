package store

import (
	"context"
	"testing"
)

func TestRoomDeleteAcceptancePersistsRemovalAndPreservesHistory(t *testing.T) {
	path := roomLegacyDatabase(t)
	s, err := Open(path)
	roomStoreCheck(t, err)
	ctx := context.Background()
	roomStoreCheck(t, s.JoinRooms(ctx, RoomMember{Room: "alpha", Agent: "worker"}, RoomMember{Room: "beta", Agent: "worker"}))
	_, err = s.db.Exec(`INSERT INTO messages(seq,ts,sender,recipient,payload) VALUES(43,'2026-10-26','worker','*','{}');
INSERT INTO message_metadata(message_id,seq,sender_session,in_reply_to,ack_requested,room) VALUES('deleted-room-history',43,'session','',1,'alpha');`)
	roomStoreCheck(t, err)
	existed, err := s.DeleteRoom(ctx, "alpha")
	roomStoreCheck(t, err)
	assertStatusEqual(t, "existing room deleted", existed, true)
	existed, err = s.DeleteRoom(ctx, "alpha")
	roomStoreCheck(t, err)
	assertStatusEqual(t, "missing room distinguished", existed, false)
	roomStoreCheck(t, s.Close())
	s, err = Open(path)
	roomStoreCheck(t, err)
	defer s.Close()
	roomScalar(t, s, "SELECT COUNT(*) FROM rooms WHERE id='alpha'", 0)
	roomScalar(t, s, "SELECT COUNT(*) FROM room_members WHERE room_id='alpha'", 0)
	roomScalar(t, s, "SELECT COUNT(*) FROM room_members WHERE room_id='beta'", 1)
	roomScalar(t, s, "SELECT room FROM message_metadata WHERE message_id='deleted-room-history'", "alpha")
	roomScalar(t, s, "SELECT COUNT(*) FROM durable_inbox WHERE message_id='legacy-parent'", 1)
	roomStoreCheck(t, s.JoinRooms(ctx, RoomMember{Room: "alpha", Agent: "new-worker"}))
	roomScalar(t, s, "SELECT COUNT(*) FROM room_members WHERE room_id='alpha'", 1)
	roomScalar(t, s, "SELECT agent_id FROM room_members WHERE room_id='alpha'", "new-worker")
}

func TestRoomDeleteAcceptanceTransactionRollsBackMembershipsOnFailure(t *testing.T) {
	s, err := Open(roomLegacyDatabase(t))
	roomStoreCheck(t, err)
	defer s.Close()
	ctx := context.Background()
	roomStoreCheck(t, s.JoinRooms(ctx, RoomMember{Room: "alpha", Agent: "worker"}))
	_, err = s.db.Exec(`CREATE TRIGGER fail_room_delete BEFORE DELETE ON rooms BEGIN SELECT RAISE(ABORT,'fixture deletion failure'); END`)
	roomStoreCheck(t, err)
	if _, err := s.DeleteRoom(ctx, "alpha"); err == nil {
		t.Fatal("injected transaction failure was hidden")
	}
	roomScalar(t, s, "SELECT COUNT(*) FROM rooms WHERE id='alpha'", 1)
	roomScalar(t, s, "SELECT COUNT(*) FROM room_members WHERE room_id='alpha'", 1)
}
