package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

const messageColumns = `cursor, contact, direction, message_id, n, created_at, COALESCE(text, ''), text_hash, hash,
    state, class, reason, COALESCE(delivered_at, ''), COALESCE(read_at, ''), receipt`

// storedRow is a row with what only the store compares.
type storedRow struct {
	messaging.Row
	textHash, hash []byte
}

type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func messageRow(ctx context.Context, q rowQuerier, contact, direction, id string) (storedRow, bool, error) {
	row := q.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM peer_messages WHERE contact = ? AND direction = ? AND message_id = ?`,
		contact, direction, id)
	r, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return storedRow{}, false, nil
	}
	return r, err == nil, err
}

func queryRows(ctx context.Context, q rowsQuerier, where string, args ...any) ([]storedRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+messageColumns+` FROM peer_messages `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedRow
	for rows.Next() {
		r, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanMessage(row rowScanner) (storedRow, error) {
	var r storedRow
	var n int64
	var created, delivered, read string
	err := row.Scan(&r.Cursor, &r.Contact, &r.Direction, &r.ID, &n, &created, &r.Text, &r.textHash, &r.hash,
		&r.State, &r.Refusal.Class, &r.Refusal.Reason, &delivered, &read, &r.Receipt)
	if err != nil {
		return storedRow{}, err
	}
	r.N = uint64(n)
	if r.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return storedRow{}, err
	}
	if r.DeliveredAt, err = optionalMessageTime(delivered); err != nil {
		return storedRow{}, err
	}
	r.ReadAt, err = optionalMessageTime(read)
	return r, err
}

func optionalMessageTime(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// msgStamp is a fixed-width UTC time, so stored times compare as text in
// time order: RFC3339Nano drops trailing zeros and would not.
func msgStamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00") }
