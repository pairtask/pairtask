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
	if err := requireContact(ctx, tx, contact); err != nil {
		return messaging.Answer{}, err
	}
	if answer, known, err := replay(ctx, tx, contact, m); known || err != nil {
		return answer, err // a replay is answered as before and stores nothing
	}
	answer, err := decideInbound(ctx, tx, contact, m)
	if err != nil || !answer.Stored {
		return answer, err
	}
	if err := insertMessage(ctx, tx, contact, messaging.DirIn, m, messaging.StateReceived, now); err != nil {
		return messaging.Answer{}, err
	}
	if err := tx.Commit(); err != nil {
		return messaging.Answer{}, err // never Stored unless it committed
	}
	return stored, nil
}

// decideInbound is the answer to a message that is not a replay; Stored
// means it may be inserted.
func decideInbound(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (messaging.Answer, error) {
	if m.N == 0 || m.N >= math.MaxInt64 || !messaging.ValidID(m.ID) {
		return refused(messaging.ReasonInvalid), nil
	}
	if old, err := belowFloor(ctx, tx, contact, m.N); old || err != nil {
		return refused(messaging.ReasonTooOld), err
	}
	if len(m.Text) > messaging.TextBytes {
		return refused(messaging.ReasonTooLarge), nil
	}
	if !messaging.ValidText(m.Text) {
		return refused(messaging.ReasonInvalid), nil
	}
	if full, err := peerInboxFull(ctx, tx, contact, len(m.Text)); full || err != nil {
		return refused(messaging.ReasonInboxFull), err
	}
	return stored, nil
}

// replay answers a message whose ID or sequence is already held: the same
// message is Stored again, anything else is a conflict.
func replay(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (messaging.Answer, bool, error) {
	row, found, err := messageRow(ctx, tx, contact, messaging.DirIn, m.ID)
	if err != nil {
		return messaging.Answer{}, false, err
	}
	hash := m.Hash()
	if found && string(row.hash) == string(hash[:]) {
		return stored, true, nil
	}
	if found {
		return refused(messaging.ReasonConflict), true, nil
	}
	if m.N == 0 || m.N >= math.MaxInt64 {
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
	return unread >= messaging.UnreadMessages || unreadBytes+size > messaging.UnreadBytes || held >= messaging.Tombstones, nil
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
