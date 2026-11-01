package store

import (
	"context"
	"database/sql"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// A review request the contact sent for one of this side's agents it may
// not ask is refused, and the refusal is recorded for this side's
// operator: one inbound row in state refused, without its text but with its
// hashes, so a replay is answered the same and a later grant never revives
// it. Nothing is imported, asked or granted, and reading it sends no
// receipt. Only this refusal is recorded; anything malformed, too large or
// too old is refused as before and leaves no trace.

// recordable: a review request refused only because it isn't shared.
func recordable(m messaging.Message, answer messaging.Answer) bool {
	return m.Review != nil && !answer.Stored && answer.Reason == messaging.ReasonNotShared
}

// noticeChecks run before a refusal is recorded: the request must be one
// this side would otherwise have taken.
var noticeChecks = []inboundCheck{
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
	{messaging.ReasonInboxFull, func(ctx context.Context, tx *sql.Tx, contact string, _ messaging.Message) (bool, error) {
		return noticesFull(ctx, tx, contact)
	}},
}

// recordRefusal records the not_shared refusal of r, unless an earlier
// check refuses it first; inserted reports a row to commit.
func recordRefusal(ctx context.Context, tx *sql.Tx, r newRow) (messaging.Answer, bool, error) {
	for _, check := range noticeChecks {
		if refuses, err := check.refuses(ctx, tx, r.key.contact, r.m); refuses || err != nil {
			return refused(check.reason), false, err
		}
	}
	refusal := messaging.Refuse(messaging.ReasonNotShared)
	if err := insertRefused(ctx, tx, r, refusal); err != nil {
		return messaging.Answer{}, false, err // never Recorded when the insert failed
	}
	return messaging.Answer{Refusal: refusal, Recorded: true}, true, nil
}

// noticesFull: the unread recorded refusals, or the remembered messages,
// are at their bounds.
func noticesFull(ctx context.Context, tx *sql.Tx, contact string) (bool, error) {
	var unread, held int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(state = 'refused' AND read_at IS NULL), 0), COUNT(*)
FROM peer_messages WHERE contact = ? AND direction = 'in'`, contact).Scan(&unread, &held)
	if err != nil {
		return false, err
	}
	return unread >= messaging.RefusedNotices || held >= messaging.Tombstones, nil
}

// insertRefused stores the refused request without its text: the hashes
// of the whole message stay, so a replay is told apart from a conflict.
func insertRefused(ctx context.Context, tx *sql.Tx, r newRow, refusal messaging.Refusal) error {
	key, m, now := r.key, r.m, r.now
	hash := m.Hash()
	_, err := tx.ExecContext(ctx, `INSERT INTO peer_messages
(contact, direction, message_id, n, created_at, text, text_bytes, text_hash, hash, state, class, reason, stored_at,
    from_agent, to_agents, reply_id, reply_author, review_ask)
VALUES (?, ?, ?, ?, ?, NULL, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.contact, key.direction, m.ID, int64(m.N), msgStamp(m.CreatedAt), textHash(m.Text), hash[:],
		messaging.StateRefused, refusal.Class, refusal.Reason, msgStamp(now), nullString(m.From), nullList(m.To),
		nullString(m.ReplyTo.ID), replyAuthor(m), reviewAskJSON(m.Review))
	return err
}
