package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This side's contact notices (#276, D3/D8): a local pause, resume or
// unpair queues one, in the transaction that makes the change, for a peer
// that can take one. A newer notice supersedes the contact's pending one,
// which is then never sent. Each attempt at delivering a notice counts on
// exactly its own row, five in total: a pending notice that reaches five
// without an answer becomes undelivered. The peer's answer marks it
// delivered, and the contact's newest delivered notice stays as evidence.

// maxNoticeAttempts is how many attempts a notice gets, the first
// included.
const maxNoticeAttempts = 5

// The states a notice carries, as stored.
const (
	noticePaused   = "paused"
	noticeResumed  = "resumed"
	noticeUnpaired = "unpaired"
)

var errNoNotice = errors.New("no such contact notice")

// NoticeDue is a pending contact notice due for an attempt: its contact,
// the pairing and generation it was queued under, the state it carries by
// its wire name, the attempts made so far and when the next is due (zero:
// now).
type NoticeDue struct {
	Contact   string
	PairingID []byte
	Gen       int64
	State     string
	Attempts  int
	NextAt    time.Time
}

// NoticeOutcome is what one notice attempt did.
type NoticeOutcome string

const (
	// NoticeSent: the notice went out in a control session, either role,
	// whether or not its answer came back.
	NoticeSent NoticeOutcome = "sent"
	// NoticeFailed: the dial failed before the notice went out.
	NoticeFailed NoticeOutcome = "failed"
)

// NoticeAttempt is one attempt at delivering the contact's notice of
// generation Gen, made At, with the next one due at Next (zero: now).
type NoticeAttempt struct {
	Contact  string
	Gen      int64
	Outcome  NoticeOutcome
	At, Next time.Time
}

// queueNotice queues the notice of state at the contact's generation for a
// peer that can take one, superseding the contact's pending notice.
func queueNotice(ctx context.Context, tx *sql.Tx, c pairingRow, state string) error {
	if !c.notified() {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE contact_notices_out SET state = 'superseded' WHERE contact = ? AND state = 'pending'`,
		c.contact); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO contact_notices_out (contact, pairing_id, gen, contact_state, state) VALUES (?, ?, ?, ?, 'pending')`,
		c.contact, c.pairingID, c.generation, state)
	return err
}

// queueChangeNotice queues the notice of a local change to the contact at
// the generation the change moved it to.
func queueChangeNotice(ctx context.Context, tx *sql.Tx, contact, state string) error {
	c, err := readPairing(ctx, tx, contact)
	if err != nil {
		return err
	}
	return queueNotice(ctx, tx, c, state)
}

const noticeDueColumns = `contact, pairing_id, gen, contact_state, attempts, next_attempt_at`

// PendingNotices are the notices due at now: pending, with attempts left
// and the next one due by now. Only the root store schedules notices.
func (p *PeerMessages) PendingNotices(ctx context.Context, now time.Time) ([]NoticeDue, error) {
	if err := p.localOnly(); err != nil {
		return nil, err
	}
	rows, err := p.db.QueryContext(ctx, `SELECT `+noticeDueColumns+` FROM contact_notices_out
WHERE state = 'pending' AND attempts < ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
ORDER BY contact, gen`, maxNoticeAttempts, msgStamp(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []NoticeDue
	for rows.Next() {
		n, err := scanNoticeDue(rows)
		if err != nil {
			return nil, err
		}
		due = append(due, n)
	}
	return due, rows.Err()
}

// RecordNoticeAttempt counts one attempt on exactly the notice of
// (a.Contact, a.Gen), whatever its delivery state, so every attempt stays
// in the evidence and a notice never spends another's budget. Only a
// pending notice is rescheduled at a.Next, and becomes undelivered at its
// fifth attempt; a delivered, undelivered or superseded one keeps its
// state and schedule. It answers the notice as it is after. Only the root
// store records attempts.
func (p *PeerMessages) RecordNoticeAttempt(ctx context.Context, a NoticeAttempt) (NoticeDue, error) {
	if err := p.localOnly(); err != nil {
		return NoticeDue{}, err
	}
	var n NoticeDue
	err := p.inTx(ctx, a.Contact, func(tx *sql.Tx) error {
		if err := countAttempt(ctx, tx, a); err != nil {
			return err
		}
		var err error
		n, err = scanNoticeDue(tx.QueryRowContext(ctx, `SELECT `+noticeDueColumns+` FROM contact_notices_out WHERE contact = ? AND gen = ?`,
			a.Contact, a.Gen))
		return err
	})
	return n, err
}

// countAttempt adds the attempt to its notice's row; SQLite reads every
// column on the right as it was before the update.
func countAttempt(ctx context.Context, tx *sql.Tx, a NoticeAttempt) error {
	res, err := tx.ExecContext(ctx, `UPDATE contact_notices_out SET attempts = attempts + 1,
    next_attempt_at = CASE WHEN state = 'pending' THEN ?3 ELSE next_attempt_at END,
    state = CASE WHEN state = 'pending' AND attempts + 1 >= ?4 THEN 'undelivered' ELSE state END
WHERE contact = ?1 AND gen = ?2`, a.Contact, a.Gen, optionalMsgStamp(a.Next), maxNoticeAttempts)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		err = fmt.Errorf("contact notice %d: %w", a.Gen, errNoNotice)
	}
	return err
}

// optionalMsgStamp is a time as msgStamp stores it, NULL for zero.
func optionalMsgStamp(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return msgStamp(t)
}

func scanNoticeDue(row rowScanner) (NoticeDue, error) {
	var n NoticeDue
	var next sql.NullString
	if err := row.Scan(&n.Contact, &n.PairingID, &n.Gen, &n.State, &n.Attempts, &next); err != nil {
		return NoticeDue{}, err
	}
	if !next.Valid {
		return n, nil
	}
	var err error
	n.NextAt, err = time.Parse(time.RFC3339Nano, next.String)
	return n, err
}

// pruneDelivered drops the contact's delivered notices older than its
// newest delivered one, which stays as evidence.
func pruneDelivered(ctx context.Context, tx *sql.Tx, contact string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM contact_notices_out WHERE contact = ?1 AND state = 'delivered'
AND gen < (SELECT MAX(gen) FROM contact_notices_out WHERE contact = ?1 AND state = 'delivered')`, contact)
	return err
}
