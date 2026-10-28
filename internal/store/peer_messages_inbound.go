package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

var stored = messaging.Answer{Stored: true}

func refused(reason string) messaging.Answer {
	return messaging.Answer{Refusal: messaging.Refuse(reason)}
}

// Accept decides one inbound message in one transaction: a replay is
// answered first, even with the inbox full; then the replay floor; then
// validity and quota; only then is it stored.
func (p *PeerMessages) Accept(ctx context.Context, contact string, m messaging.Message, now time.Time) (messaging.Answer, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return messaging.Answer{}, err
	}
	defer tx.Rollback()
	answer, inserted, err := acceptIn(ctx, tx, newRow{messageKey{contact, messaging.DirIn, m.ID}, m, now})
	if err != nil || !inserted {
		return answer, err
	}
	if err := tx.Commit(); err != nil {
		return messaging.Answer{}, err // never Stored unless it committed
	}
	return stored, nil
}

// acceptIn is Accept inside its transaction; inserted reports a new row to
// commit. A replay is answered as before and stores nothing.
func acceptIn(ctx context.Context, tx *sql.Tx, r newRow) (messaging.Answer, bool, error) {
	contact, m := r.key.contact, r.m
	if err := requireContact(ctx, tx, contact); err != nil {
		return messaging.Answer{}, false, err
	}
	if answer, known, err := replay(ctx, tx, contact, m); known || err != nil {
		return answer, false, err
	}
	answer, err := decideInbound(ctx, tx, contact, m)
	if err != nil || !answer.Stored {
		return answer, false, err
	}
	if err := insertMessage(ctx, tx, r); err != nil {
		return messaging.Answer{}, false, err // never Stored when the insert failed
	}
	return stored, true, nil
}

// inboundCheck refuses a message for one reason, or passes it on.
type inboundCheck struct {
	reason  string
	refuses func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error)
}

// inboundChecks run in order after the replay lookup: identity, the replay
// floor, the text, then the quota.
var inboundChecks = []inboundCheck{
	{messaging.ReasonInvalid, func(_ context.Context, _ *sql.Tx, _ string, m messaging.Message) (bool, error) {
		return !validSequence(m.N) || !messaging.ValidID(m.ID), nil
	}},
	{messaging.ReasonTooOld, func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
		return belowFloor(ctx, tx, contact, m.N)
	}},
	{messaging.ReasonTooLarge, func(_ context.Context, _ *sql.Tx, _ string, m messaging.Message) (bool, error) {
		return len(m.Text) > messaging.TextBytes, nil
	}},
	{messaging.ReasonInvalid, func(_ context.Context, _ *sql.Tx, _ string, m messaging.Message) (bool, error) {
		return !messaging.ValidText(m.Text), nil
	}},
	{messaging.ReasonInboxFull, func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
		return peerInboxFull(ctx, tx, contact, len(m.Text))
	}},
}

// decideInbound is the answer to a message that is not a replay; Stored
// means it may be inserted.
func decideInbound(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (messaging.Answer, error) {
	for _, check := range inboundChecks {
		if refuses, err := check.refuses(ctx, tx, contact, m); refuses || err != nil {
			return refused(check.reason), err
		}
	}
	return stored, nil
}

// validSequence: from 1, within what the store can hold.
func validSequence(n uint64) bool { return n != 0 && n < math.MaxInt64 }

// replay answers a message whose ID or sequence is already held: the same
// message is Stored again, anything else is a conflict.
func replay(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (messaging.Answer, bool, error) {
	row, found, err := messageRow(ctx, tx, messageKey{contact, messaging.DirIn, m.ID})
	if err != nil {
		return messaging.Answer{}, false, err
	}
	if found {
		return sameMessage(row, m), true, nil
	}
	if !validSequence(m.N) {
		return messaging.Answer{}, false, nil
	}
	var other int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_messages WHERE contact = ? AND direction = 'in' AND n = ?`,
		contact, int64(m.N)).Scan(&other)
	if err != nil || other == 0 {
		return messaging.Answer{}, false, err
	}
	return refused(messaging.ReasonConflict), true, nil
}

// sameMessage answers a message whose ID is held: Stored again when it is
// the very same message, a conflict otherwise.
func sameMessage(row storedRow, m messaging.Message) messaging.Answer {
	hash := m.Hash()
	if string(row.hash) == string(hash[:]) {
		return stored
	}
	return refused(messaging.ReasonConflict)
}

// belowFloor: the sequence is older than anything this side still
// remembers, so it can't be told apart from a replay.
func belowFloor(ctx context.Context, tx *sql.Tx, contact string, n uint64) (bool, error) {
	floor, err := replayFloor(ctx, tx, contact)
	return int64(n) < floor, err
}

func replayFloor(ctx context.Context, q rowQuerier, contact string) (int64, error) {
	var floor int64
	err := q.QueryRowContext(ctx, `SELECT floor FROM peer_message_floors WHERE contact = ?`, contact).Scan(&floor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return floor, err
}

// peerInboxFull: the unread messages or the remembered ones are at their bounds.
func peerInboxFull(ctx context.Context, tx *sql.Tx, contact string, size int) (bool, error) {
	var unread, unreadBytes, held int
	err := tx.QueryRowContext(ctx, `SELECT
    COALESCE(SUM(state = 'received'), 0), COALESCE(SUM(CASE WHEN state = 'received' THEN text_bytes END), 0), COUNT(*)
FROM peer_messages WHERE contact = ? AND direction = 'in'`, contact).Scan(&unread, &unreadBytes, &held)
	if err != nil {
		return false, err
	}
	return !unreadBudget.holds(unread, unreadBytes+size) || held >= messaging.Tombstones, nil
}

// MarkRead marks inbound messages read as shown here, and queues a receipt
// for each one newly read; the oldest pending receipts past the bound are
// dropped.
func (p *PeerMessages) MarkRead(ctx context.Context, contact string, ids []string, at time.Time) (int, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	marked := 0
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `UPDATE peer_messages SET state = 'read', read_at = ?1, receipt = 'pending', receipt_at = ?1
WHERE contact = ?2 AND direction = 'in' AND message_id = ?3 AND state = 'received'`, msgStamp(at), contact, id)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		marked += int(n)
	}
	if err := dropReceiptsOver(ctx, tx, contact); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return marked, nil
}

// dropReceiptsOver drops the oldest pending receipts beyond the bound.
func dropReceiptsOver(ctx context.Context, tx *sql.Tx, contact string) error {
	_, err := tx.ExecContext(ctx, `UPDATE peer_messages SET receipt = 'dropped'
WHERE cursor IN (SELECT cursor FROM peer_messages WHERE contact = ?1 AND direction = 'in' AND receipt = 'pending'
    ORDER BY receipt_at DESC, cursor DESC LIMIT -1 OFFSET ?2)`, contact, messaging.Receipts)
	return err
}

// PendingReads is the oldest receipts not yet confirmed by the peer.
func (p *PeerMessages) PendingReads(ctx context.Context, contact string, max int) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT message_id FROM peer_messages
WHERE contact = ? AND direction = 'in' AND receipt = 'pending' ORDER BY receipt_at, cursor LIMIT ?`,
		contact, clamp(max, messaging.SessionMessages))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ReadConfirmed records the peer's ReadStored for a pending receipt.
func (p *PeerMessages) ReadConfirmed(ctx context.Context, contact, id string) error {
	_, err := p.db.ExecContext(ctx, `UPDATE peer_messages SET receipt = 'confirmed'
WHERE contact = ? AND direction = 'in' AND message_id = ? AND receipt = 'pending'`, contact, id)
	return err
}
