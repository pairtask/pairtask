package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// Collect keeps every contact's messages within their bounds, one contact
// per transaction: queued messages expire, old pending receipts drop, text
// beyond the retention bounds is collected (never an unread message's or a
// queued one's), and the replay floor rises past old tombstones.
func (p *PeerMessages) Collect(ctx context.Context, now time.Time) error {
	contacts, err := messageContacts(ctx, p.db)
	if err != nil {
		return err
	}
	for _, contact := range contacts {
		if err := p.collectContact(ctx, contact, now); err != nil {
			return err
		}
	}
	return nil
}

func messageContacts(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT contact FROM peer_messages ORDER BY contact`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (p *PeerMessages) collectContact(ctx context.Context, contact string, now time.Time) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, step := range []func(context.Context, *sql.Tx, string, time.Time) error{
		expireQueued, expireReceipts, collectText, raiseFloor,
	} {
		if err := step(ctx, tx, contact, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func expireReceipts(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE peer_messages SET receipt = 'dropped'
WHERE contact = ? AND direction = 'in' AND receipt = 'pending' AND receipt_at < ?`, contact, msgStamp(now.Add(-messaging.ReceiptAge)))
	if err != nil {
		return err
	}
	return dropReceiptsOver(ctx, tx, contact)
}

// collectText drops the text of the oldest settled messages beyond the
// retention bounds: inbound ones read here, outbound ones no longer queued.
// The row, its sequence and its hashes stay.
func collectText(ctx context.Context, tx *sql.Tx, contact string, _ time.Time) error {
	texts, err := settledTexts(ctx, tx, contact)
	if err != nil {
		return err
	}
	for _, cursor := range beyondRetention(texts) {
		if _, err := tx.ExecContext(ctx, `UPDATE peer_messages SET text = NULL WHERE cursor = ?`, cursor); err != nil {
			return err
		}
	}
	return nil
}

// heldText is one stored text: its row and size.
type heldText struct {
	cursor int64
	size   int
}

// settledTexts are the texts that may be collected, newest first.
func settledTexts(ctx context.Context, tx *sql.Tx, contact string) ([]heldText, error) {
	rows, err := tx.QueryContext(ctx, `SELECT cursor, text_bytes FROM peer_messages
WHERE contact = ? AND text IS NOT NULL AND (
    (direction = 'in' AND state = 'read') OR (direction = 'out' AND state IN ('delivered', 'read', 'expired', 'refused')))
ORDER BY cursor DESC`, contact)
	if err != nil {
		return nil, err
	}
	var out []heldText
	for rows.Next() {
		var t heldText
		if err := rows.Scan(&t.cursor, &t.size); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	return out, closeRows(rows) // an incomplete read collects nothing
}

// beyondRetention are the rows past the newest RetainedText texts within
// RetainedTextBytes.
func beyondRetention(texts []heldText) []int64 {
	var drop []int64
	kept, bytes := 0, 0
	for _, t := range texts {
		if fits(kept, bytes+t.size, messaging.RetainedText, messaging.RetainedTextBytes) {
			kept, bytes = kept+1, bytes+t.size
			continue
		}
		drop = append(drop, t.cursor)
	}
	return drop
}

// raiseFloor forgets the oldest inbound tombstones, lowest sequence first,
// while each is past TombstoneAge, read and without a pending receipt; the
// floor then sits just above the last one forgotten, so their sequences are
// refused as too old rather than taken as new. It stops at the first row
// that must stay, so it never passes a retained message.
func raiseFloor(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	forget, last, err := forgettable(ctx, tx, contact, now)
	if err != nil || len(forget) == 0 {
		return err // an incomplete read forgets nothing
	}
	for _, cursor := range forget {
		if _, err := tx.ExecContext(ctx, `DELETE FROM peer_messages WHERE cursor = ?`, cursor); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO peer_message_floors (contact, floor) VALUES (?, ?)
ON CONFLICT(contact) DO UPDATE SET floor = MAX(floor, excluded.floor)`, contact, last+1)
	return err
}

// forgettable is the leading run of inbound rows, by sequence, that may be
// forgotten, and the last one's sequence.
func forgettable(ctx context.Context, tx *sql.Tx, contact string, now time.Time) ([]int64, int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT cursor, n, stored_at < ? AND state = 'read' AND receipt != 'pending'
FROM peer_messages WHERE contact = ? AND direction = 'in' ORDER BY n`, msgStamp(now.Add(-messaging.TombstoneAge)), contact)
	if err != nil {
		return nil, 0, err
	}
	var forget []int64
	var last int64
	for rows.Next() {
		var cursor, n int64
		var old bool
		if err := rows.Scan(&cursor, &n, &old); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if !old {
			break
		}
		forget, last = append(forget, cursor), n
	}
	if err := closeRows(rows); err != nil {
		return nil, 0, err
	}
	return forget, last, nil
}

// closeRows ends a read, reporting an iteration error before a close one.
func closeRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}
