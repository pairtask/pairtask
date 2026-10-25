package store

import (
	"context"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// roomsSchema records project rooms (#153) and who is in them. A message's
// room is in message_metadata.room; NULL is the Square (everyone), so every
// message from before rooms is already correct.
const roomsSchema = `
CREATE TABLE IF NOT EXISTS rooms (
    id         TEXT PRIMARY KEY,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS room_members (
    room_id   TEXT NOT NULL,
    agent_id  TEXT NOT NULL,
    joined_at TEXT NOT NULL,
    PRIMARY KEY (room_id, agent_id)
);
CREATE INDEX IF NOT EXISTS room_members_by_agent ON room_members(agent_id, room_id);
`

// CreateRoom adds an empty room; it reports whether the room is new.
func (s *Store) CreateRoom(ctx context.Context, room string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO rooms (id, created_at) VALUES (?, ?)`, room, stamp(time.Now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n != 0, err
}

// RoomMember is one agent in one room.
type RoomMember struct {
	Room, Agent string
}

// JoinRooms adds each membership, creating rooms that don't exist. Joining is
// additive: existing memberships stay.
func (s *Store) JoinRooms(ctx context.Context, members ...RoomMember) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := stamp(time.Now())
	for _, m := range members {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO rooms (id, created_at) VALUES (?, ?)`, m.Room, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO room_members (room_id, agent_id, joined_at) VALUES (?, ?, ?)`, m.Room, m.Agent, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LeaveRoom removes the membership. Deliveries already accepted stay.
func (s *Store) LeaveRoom(ctx context.Context, m RoomMember) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM room_members WHERE room_id = ? AND agent_id = ?`, m.Room, m.Agent)
	return err
}

// AgentRooms is the agent's rooms, sorted.
func (s *Store) AgentRooms(ctx context.Context, agent string) ([]string, error) {
	return s.column(ctx, `SELECT room_id FROM room_members WHERE agent_id = ? ORDER BY room_id`, agent)
}

// RoomExists reports whether a room was created.
func (s *Store) RoomExists(ctx context.Context, room string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rooms WHERE id = ?`, room).Scan(&n)
	return n != 0, err
}

// Rooms lists the rooms in ID order, empty ones included, at most
// ListedRooms with at most ListedRoomMembers members each, with the totals.
func (s *Store) Rooms(ctx context.Context) (protocol.RoomList, error) {
	list := protocol.RoomList{Rooms: []protocol.RoomInfo{}}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rooms`).Scan(&list.Total); err != nil {
		return list, err
	}
	ids, err := s.column(ctx, `SELECT id FROM rooms ORDER BY id LIMIT ?`, protocol.ListedRooms)
	if err != nil {
		return list, err
	}
	for _, id := range ids {
		room, err := s.roomInfo(ctx, id)
		if err != nil {
			return list, err
		}
		list.Rooms = append(list.Rooms, room)
	}
	list.Truncated = list.Total > len(list.Rooms)
	return list, nil
}

func (s *Store) roomInfo(ctx context.Context, id string) (protocol.RoomInfo, error) {
	room := protocol.RoomInfo{ID: id}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM room_members WHERE room_id = ?`, id).Scan(&room.MembersTotal); err != nil {
		return room, err
	}
	members, err := s.column(ctx, `SELECT agent_id FROM room_members WHERE room_id = ? ORDER BY agent_id LIMIT ?`, id, protocol.ListedRoomMembers)
	room.Members, room.MembersTruncated = members, room.MembersTotal > len(members)
	return room, err
}
