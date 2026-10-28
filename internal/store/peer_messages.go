package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// peerMessagesSchema keeps messages between paired workspaces (#210), both
// directions, one row each. A message's ID, sequence, time and hash never
// change; its text is collected within the bounds, and an inbound row is
// the tombstone that stops a replay until the floor passes it.
const peerMessagesSchema = `
CREATE TABLE IF NOT EXISTS peer_messages (
    cursor       INTEGER PRIMARY KEY AUTOINCREMENT,
    contact      TEXT NOT NULL,
    direction    TEXT NOT NULL,
    message_id   TEXT NOT NULL,
    n            INTEGER NOT NULL,
    created_at   TEXT NOT NULL,
    text         TEXT,
    text_bytes   INTEGER NOT NULL,
    text_hash    BLOB NOT NULL,
    hash         BLOB NOT NULL,
    state        TEXT NOT NULL,
    class        TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',
    stored_at    TEXT NOT NULL,
    delivered_at TEXT,
    read_at      TEXT,
    receipt      TEXT NOT NULL DEFAULT '',
    receipt_at   TEXT,
    UNIQUE (contact, direction, message_id),
    UNIQUE (contact, direction, n)
);
CREATE INDEX IF NOT EXISTS peer_messages_state ON peer_messages (contact, direction, state);
CREATE TABLE IF NOT EXISTS peer_message_sequences (
    contact TEXT PRIMARY KEY,
    next    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS peer_message_floors (
    contact TEXT PRIMARY KEY,
    floor   INTEGER NOT NULL
);
`

// messagePageLimit bounds one Page.
const messagePageLimit = 64

// PeerMessages is the store's messages between paired workspaces.
type PeerMessages struct{ db *sql.DB }

var _ messaging.Store = (*PeerMessages)(nil)

// PeerMessages is the messaging store over this database.
func (s *Store) PeerMessages() *PeerMessages { return &PeerMessages{db: s.db} }

// confirmedContact: a contact row and a confirmed decision, as the inviter
// (a delivered acceptance) or as the joiner (a confirmed contact join). A
// pending or rejected pairing never counts.
func confirmedContact(ctx context.Context, q rowQuerier, contact contactKey) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM peer_contacts WHERE identity_key = ?1) AND (
    EXISTS (SELECT 1 FROM peer_pairing_requests WHERE identity_key = ?1 AND state = 'accepted' AND delivered_at IS NOT NULL)
    OR EXISTS (SELECT 1 FROM peer_joins WHERE peer = ?1 AND state = 'contact' AND decision_confirmed_at IS NOT NULL))`,
		string(contact)).Scan(&ok)
	return ok, err
}

// contactKey is a contact's identity key.
type contactKey string

func requireContact(ctx context.Context, q rowQuerier, contact contactKey) error {
	ok, err := confirmedContact(ctx, q, contact)
	if err == nil && !ok {
		err = messaging.ErrUnknownContact
	}
	return err
}

func textHash(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	return sum[:]
}

// Queue stores an outbound message with the contact's next sequence.
func (p *PeerMessages) Queue(ctx context.Context, o messaging.Outgoing) (messaging.Row, error) {
	if !messaging.ValidID(o.ID) || !messaging.ValidText(o.Text) {
		return messaging.Row{}, fmt.Errorf("%w: ID or text", messaging.ErrInvalid)
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return messaging.Row{}, err
	}
	defer tx.Rollback()
	key := messageKey{o.Contact, messaging.DirOut, o.ID}
	row, err := queueIn(ctx, tx, outgoing{key, o.Text, o.Now})
	if err != nil {
		return messaging.Row{}, err
	}
	if err := tx.Commit(); err != nil {
		return messaging.Row{}, err
	}
	return row, nil
}

// outgoing is one message the operator queues.
type outgoing struct {
	key  messageKey
	text string
	now  time.Time
}

// queueIn is Queue inside its transaction: the existing row for a replay,
// or a new one with the next sequence.
func queueIn(ctx context.Context, tx *sql.Tx, o outgoing) (messaging.Row, error) {
	key, text, now := o.key, o.text, o.now
	if err := requireContact(ctx, tx, contactKey(key.contact)); err != nil {
		return messaging.Row{}, err
	}
	row, found, err := messageRow(ctx, tx, key)
	if err != nil || found {
		return replayed(row, o, err)
	}
	if err := outboxRoom(ctx, tx, o); err != nil {
		return messaging.Row{}, err
	}
	n, err := nextSequence(ctx, tx, contactKey(key.contact))
	if err != nil {
		return messaging.Row{}, err
	}
	m := messaging.Message{ID: key.id, N: n, CreatedAt: now.UTC(), Text: text}
	if err := insertMessage(ctx, tx, newRow{key, m, now}); err != nil {
		return messaging.Row{}, err
	}
	row, _, err = messageRow(ctx, tx, key)
	return row.Row, err
}

// replayed answers a Queue of an ID already queued: the same text is the
// same message, other text a conflict.
func replayed(row storedRow, o outgoing, err error) (messaging.Row, error) {
	switch {
	case err != nil:
		return messaging.Row{}, err
	case string(textHash(o.text)) != string(row.textHash):
		return messaging.Row{}, messaging.ErrConflict
	}
	return row.Row, nil
}

// outboxRoom refuses a message the contact's queued messages leave no room for.
func outboxRoom(ctx context.Context, tx *sql.Tx, o outgoing) error {
	var held budget
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(text_bytes), 0) FROM peer_messages
WHERE contact = ? AND direction = 'out' AND state = 'queued'`, o.key.contact).Scan(&held.count, &held.bytes)
	if err != nil {
		return err
	}
	if !outboxBudget.holds(budget{held.count, held.bytes + len(o.text)}) {
		return messaging.ErrOutboxFull
	}
	return nil
}

// budget is a bound on messages and on their bytes.
type budget struct{ count, bytes int }

var (
	outboxBudget    = budget{messaging.OutboxMessages, messaging.OutboxBytes}
	unreadBudget    = budget{messaging.UnreadMessages, messaging.UnreadBytes}
	retentionBudget = budget{messaging.RetainedText, messaging.RetainedTextBytes}
)

// holds: a count below the bound and bytes within it.
func (b budget) holds(used budget) bool {
	return used.count < b.count && used.bytes <= b.bytes
}

// clamp is a caller's budget kept within this one, never negative.
func (b budget) clamp(asked budget) budget {
	return budget{max(0, min(asked.count, b.count)), max(0, min(asked.bytes, b.bytes))}
}

// sessionBudget is what one session may carry each way.
var sessionBudget = budget{messaging.SessionMessages, messaging.SessionBytes}

// nextSequence takes the contact's next sequence, from 1; it is never
// reused and never wraps.
func nextSequence(ctx context.Context, tx *sql.Tx, contact contactKey) (uint64, error) {
	var next int64 = 1
	err := tx.QueryRowContext(ctx, `SELECT next FROM peer_message_sequences WHERE contact = ?`, string(contact)).Scan(&next)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if next < 1 || next == math.MaxInt64 {
		return 0, errors.New("messaging: sequence exhausted for this contact")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO peer_message_sequences (contact, next) VALUES (?, ?)
ON CONFLICT(contact) DO UPDATE SET next = excluded.next`, string(contact), next+1)
	return uint64(next), err
}

// initialState is a new row's state by direction.
var initialState = map[string]string{messaging.DirOut: messaging.StateQueued, messaging.DirIn: messaging.StateReceived}

// newRow is a message to insert: its key, the message and when.
type newRow struct {
	key messageKey
	m   messaging.Message
	now time.Time
}

func insertMessage(ctx context.Context, tx *sql.Tx, r newRow) error {
	key, m, now := r.key, r.m, r.now
	hash := m.Hash()
	contact, direction, state := key.contact, key.direction, initialState[key.direction]
	_, err := tx.ExecContext(ctx, `INSERT INTO peer_messages
(contact, direction, message_id, n, created_at, text, text_bytes, text_hash, hash, state, stored_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		contact, direction, m.ID, int64(m.N), msgStamp(m.CreatedAt), m.Text, len(m.Text), textHash(m.Text), hash[:], state, msgStamp(now))
	return err
}

// Offer is the oldest queued messages that fit the session bounds; queued
// messages past QueuedExpiry are expired first and never offered.
func (p *PeerMessages) Offer(ctx context.Context, contact string, now time.Time, l messaging.Limit) ([]messaging.Row, error) {
	limit := sessionBudget.clamp(budget{l.Count, l.Bytes})
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := expireQueued(ctx, tx, contact, now); err != nil {
		return nil, err
	}
	rows, err := queryRows(ctx, tx, `WHERE contact = ? AND direction = 'out' AND state = 'queued' ORDER BY n`, contact)
	if err != nil {
		return nil, err
	}
	out := within(rows, budget{limit.count + 1, limit.bytes})
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// within is the leading rows whose count and text the budget holds.
func within(rows []storedRow, b budget) []messaging.Row {
	var out []messaging.Row
	bytes := 0
	for _, r := range rows {
		bytes += len(r.Text)
		if !b.holds(budget{len(out) + 1, bytes}) {
			break
		}
		out = append(out, r.Row)
	}
	return out
}

func expireQueued(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE peer_messages SET state = 'expired'
WHERE contact = ? AND direction = 'out' AND state = 'queued' AND created_at < ?`, contact, msgStamp(now.Add(-messaging.QueuedExpiry)))
	return err
}

// Delivered records the peer's Stored: a queued or expired message is
// delivered; a read one stays read, and the time is kept once set.
func (p *PeerMessages) Delivered(ctx context.Context, contact, id string, at time.Time) error {
	_, err := p.db.ExecContext(ctx, `UPDATE peer_messages SET
    delivered_at = COALESCE(delivered_at, ?),
    state = CASE WHEN state IN ('queued', 'expired') THEN 'delivered' ELSE state END,
    class = CASE WHEN state IN ('queued', 'expired') THEN '' ELSE class END,
    reason = CASE WHEN state IN ('queued', 'expired') THEN '' ELSE reason END
WHERE contact = ? AND direction = 'out' AND message_id = ? AND state IN ('queued', 'expired', 'delivered', 'read')`,
		msgStamp(at), contact, id)
	return err
}

// Refused records a refusal of a queued message: a terminal one ends it, a
// transient one keeps it queued with the reason.
func (p *PeerMessages) Refused(ctx context.Context, contact string, refused messaging.Refused) error {
	id, r := refused.ID, refused.Refusal
	if !r.Valid() {
		return fmt.Errorf("%w: refusal", messaging.ErrInvalid)
	}
	state := messaging.StateQueued
	if r.Class == messaging.Terminal {
		state = messaging.StateRefused
	}
	_, err := p.db.ExecContext(ctx, `UPDATE peer_messages SET state = ?, class = ?, reason = ?
WHERE contact = ? AND direction = 'out' AND message_id = ? AND state = 'queued'`, state, r.Class, r.Reason, contact, id)
	return err
}

// PeerRead records the peer's Read of one of this side's messages to it; a
// read message was stored, so it is delivered too. Refused messages and
// anything else are left alone.
func (p *PeerMessages) PeerRead(ctx context.Context, contact, id string, at time.Time) error {
	_, err := p.db.ExecContext(ctx, `UPDATE peer_messages SET state = 'read',
    read_at = COALESCE(read_at, ?1), delivered_at = COALESCE(delivered_at, ?1), class = '', reason = ''
WHERE contact = ?2 AND direction = 'out' AND message_id = ?3 AND state IN ('queued', 'expired', 'delivered', 'read')`,
		msgStamp(at), contact, id)
	return err
}

// Page is a contact's messages, both directions, newest first, before a
// cursor (0 or less for the newest).
func (p *PeerMessages) Page(ctx context.Context, contact string, before int64, limit int) ([]messaging.Row, bool, error) {
	limit = max(1, min(limit, messagePageLimit))
	if before <= 0 {
		before = math.MaxInt64
	}
	rows, err := queryRows(ctx, p.db, `WHERE contact = ? AND cursor < ? ORDER BY cursor DESC LIMIT ?`, contact, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	out := make([]messaging.Row, 0, min(len(rows), limit))
	for _, r := range rows[:min(len(rows), limit)] {
		out = append(out, r.Row)
	}
	return out, more, nil
}

// Confirmed reports a confirmed contact: the only identities a message
// session is accepted from or dialed to.
func (p *PeerMessages) Confirmed(ctx context.Context, contact string) (bool, error) {
	return confirmedContact(ctx, p.db, contactKey(contact))
}
