package store

import (
	"context"
	"database/sql"
)

// MessageRoom is the room a stored message was sent to: "" for the Square
// or an unknown message.
func (s *Store) MessageRoom(ctx context.Context, messageID string) (string, error) {
	var room sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT room FROM message_metadata WHERE message_id = ?`, messageID).Scan(&room)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return room.String, err
}

// column reads one text column.
func (s *Store) column(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RoomMembers is the room's members.
func (s *Store) RoomMembers(ctx context.Context, room string) ([]string, error) {
	return s.column(ctx, `SELECT agent_id FROM room_members WHERE room_id = ? ORDER BY agent_id`, room)
}
