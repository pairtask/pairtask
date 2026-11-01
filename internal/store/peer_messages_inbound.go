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
	answer, inserted, err := acceptIn(ctx, tx, newRow{key: messageKey{contact, messaging.DirIn, m.ID}, m: m, now: now})
	if err != nil || !inserted {
		return answer, err
	}
	if err := tx.Commit(); err != nil {
		return messaging.Answer{}, err // never Stored or Recorded unless it committed
	}
	return answer, nil
}

// acceptIn is Accept inside its transaction; inserted reports a new row to
// commit. A replay is answered as before and stores nothing.
func acceptIn(ctx context.Context, tx *sql.Tx, r newRow) (messaging.Answer, bool, error) {
	contact, m := r.key.contact, r.m
	if err := requireContact(ctx, tx, contactKey(contact)); err != nil {
		return messaging.Answer{}, false, err
	}
	if answer, known, err := replay(ctx, tx, contact, m); known || err != nil {
		return answer, false, err
	}
	answer, err := decideInbound(ctx, tx, contact, m)
	if err == nil && recordable(m, answer) {
		return recordRefusal(ctx, tx, r)
	}
	if err != nil || !answer.Stored {
		return answer, false, err
	}
	if err := insertMessage(ctx, tx, r); err != nil {
		return messaging.Answer{}, false, err // never Stored when the insert failed
	}
	if err := stageImport(ctx, tx, r); err != nil {
		return messaging.Answer{}, false, err
	}
	return stored, true, nil
}

// inboundCheck refuses a message for one reason, or passes it on.
type inboundCheck struct {
	reason  string
	refuses func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error)
}

// inboundChecks run in order after the replay lookup: identity and
// routing, the recipients' grants, the replay floor, the text, then the
// quota.
var inboundChecks = []inboundCheck{
	{messaging.ReasonInvalid, func(_ context.Context, _ *sql.Tx, _ string, m messaging.Message) (bool, error) {
		if !validSequence(m.N) || !messaging.ValidID(m.ID) {
			return true, nil
		}
		return !messaging.ValidRouting(m) || !validAsk(m), nil
	}},
	{messaging.ReasonNotShared, func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
		ok, err := recipientsGranted(ctx, tx, contact, m)
		return !ok, err
	}},
	{messaging.ReasonConflict, func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
		return importedBefore(ctx, tx, contact, m)
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
		if len(m.To) > 0 {
			return importBacklogFull(ctx, tx, contact, len(m.Text))
		}
		return peerInboxFull(ctx, tx, contact, len(m.Text))
	}},
	{messaging.ReasonInboxFull, func(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
		if m.Review == nil {
			return false, nil
		}
		if full, err := outcomesFull(ctx, tx, contact); full || err != nil {
			return full, err
		}
		return asksFull(ctx, tx, contact, m.To[0])
	}},
}

// recipientsGranted: a message's recipients are shared with the contact;
// a review request's one agent may be asked by it, whatever it shares.
func recipientsGranted(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
	if m.Review == nil {
		return allGranted(ctx, tx, contact, m.To)
	}
	_, granted, err := reviewGrant(ctx, tx, contact, m.To[0])
	return granted, err
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

// sameMessage answers a message whose ID is held: the very same message is
// answered as before, Stored again or refused as recorded; anything else
// is a conflict.
func sameMessage(row storedRow, m messaging.Message) messaging.Answer {
	hash := m.Hash()
	switch {
	case string(row.hash) != string(hash[:]):
		return refused(messaging.ReasonConflict)
	case row.State == messaging.StateRefused:
		return messaging.Answer{Refusal: row.Refusal}
	}
	return stored
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
	return !unreadBudget.holds(budget{unread, unreadBytes + size}) || held >= messaging.Tombstones, nil
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
	marked, err := markReadIn(ctx, tx, readSet{contact, ids, at})
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return marked, nil
}

// readSet is received messages shown to the operator at a time.
type readSet struct {
	contact string
	ids     []string
	at      time.Time
}

// markReadIn is MarkRead inside a transaction.
func markReadIn(ctx context.Context, tx *sql.Tx, r readSet) (int, error) {
	contact, ids, at := r.contact, r.ids, r.at
	marked := 0
	for _, id := range ids {
		n, err := markOneRead(ctx, tx, readOne{contact, id, at})
		if err != nil {
			return 0, err
		}
		marked += n
	}
	if err := dropReceiptsOver(ctx, tx, contact); err != nil {
		return 0, err
	}
	return marked, nil
}

// readOne is one inbound message shown to the operator at a time.
type readOne struct {
	contact, id string
	at          time.Time
}

// markOneRead marks a received message read with a receipt for the peer,
// or a refusal recorded here read once, with none: the peer already knows.
func markOneRead(ctx context.Context, tx *sql.Tx, r readOne) (int, error) {
	res, err := tx.ExecContext(ctx, `UPDATE peer_messages SET
    state = CASE WHEN state = 'received' THEN 'read' ELSE state END, read_at = ?1,
    receipt = CASE WHEN state = 'received' THEN 'pending' ELSE receipt END,
    receipt_at = CASE WHEN state = 'received' THEN ?1 ELSE receipt_at END
WHERE contact = ?2 AND direction = 'in' AND message_id = ?3
    AND (state = 'received' OR state = 'refused' AND read_at IS NULL)`, msgStamp(r.at), r.contact, r.id)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
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
		contact, sessionBudget.clamp(budget{max, 0}).count)
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
