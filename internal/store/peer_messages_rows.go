package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

const messageColumns = `cursor, contact, direction, message_id, n, created_at, COALESCE(text, ''), text_hash, hash,
    state, class, reason, COALESCE(delivered_at, ''), COALESCE(read_at, ''), receipt,
    COALESCE(from_agent, ''), to_agents, COALESCE(reply_id, ''), COALESCE(reply_author, 0), review_ask`

// storedRow is a row with what only the store compares.
type storedRow struct {
	messaging.Row
	textHash, hash []byte
}

type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// messageKey names one message: a contact, a direction and an ID.
type messageKey struct{ contact, direction, id string }

func messageRow(ctx context.Context, q rowQuerier, k messageKey) (storedRow, bool, error) {
	row := q.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM peer_messages WHERE contact = ? AND direction = ? AND message_id = ?`,
		k.contact, k.direction, k.id)
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
	var to, ask sql.NullString
	err := row.Scan(&r.Cursor, &r.Contact, &r.Direction, &r.ID, &n, &created, &r.Text, &r.textHash, &r.hash,
		&r.State, &r.Refusal.Class, &r.Refusal.Reason, &delivered, &read, &r.Receipt,
		&r.From, &to, &r.ReplyTo.ID, &r.ReplyTo.Author, &ask)
	if err != nil {
		return storedRow{}, err
	}
	if r.Review, err = scanReviewAsk(ask); err != nil {
		return storedRow{}, err
	}
	if r.To, err = groupList(to); err != nil {
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

// AgentAcks is the contact's agents that acknowledged this side's message id.
func (p *PeerMessages) AgentAcks(ctx context.Context, contact, id string) ([]string, error) {
	return queryStrings(ctx, p.db, `SELECT agent FROM peer_agent_acks WHERE contact = ? AND message_id = ? ORDER BY agent`, contact, id)
}
