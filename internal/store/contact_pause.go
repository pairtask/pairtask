package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// Pausing a contact (#276) blocks new traffic with it, both ways, keeping
// the contact, its grants and its history. A session is admitted under the
// contact's generation; pausing and resuming each move the generation on,
// and every transaction a session makes checks it first, in the same
// transaction as its work. The database has one connection, so every
// transaction is ordered before or after a pause: one before it happened
// before the pause, one after it finds another generation and writes
// nothing.

var (
	// ErrPaused is a contact this side's operator paused: resume it first.
	ErrPaused = errors.New("the contact is paused; resume it first")
	// ErrFenced is a session admitted before the contact was paused or
	// resumed, or used for another contact: it may change nothing more.
	ErrFenced = errors.New("the contact was paused or resumed since this session was admitted")
)

// linkFence is the contact and generation a session was admitted under.
type linkFence struct {
	contact    string
	generation int64
}

// contactState is what admission and the fence check: whether the contact
// is confirmed, whether it is paused, and its generation.
type contactState struct {
	confirmed, paused bool
	generation        int64
}

func readContactState(ctx context.Context, q rowQuerier, contact string) (contactState, error) {
	confirmed, err := confirmedContact(ctx, q, contactKey(contact))
	if err != nil || !confirmed {
		return contactState{}, err
	}
	s := contactState{confirmed: true}
	var pausedAt string
	err = q.QueryRowContext(ctx, `SELECT paused_at, generation FROM peer_contacts WHERE identity_key = ?`, contact).Scan(&pausedAt, &s.generation)
	s.paused = pausedAt != ""
	return s, err
}

// Admit binds a session to a confirmed, active contact at its generation.
// Only the root store admits: a bound one can't refresh or move its own.
func (p *PeerMessages) Admit(ctx context.Context, contact string) (*PeerMessages, error) {
	if p.fence != nil {
		return nil, ErrFenced
	}
	var s contactState
	err := p.inTx(ctx, contact, func(tx *sql.Tx) (err error) {
		s, err = readContactState(ctx, tx, contact)
		return err
	})
	switch {
	case err != nil:
		return nil, err
	case !s.confirmed:
		return nil, messaging.ErrUnknownContact
	case s.paused:
		return nil, ErrPaused
	}
	return &PeerMessages{db: p.db, fence: &linkFence{contact: contact, generation: s.generation}}, nil
}

// Fence is the contact and generation this store is bound to; bound is
// false for the root store.
func (p *PeerMessages) Fence() (contact string, generation int64, bound bool) {
	if p.fence == nil {
		return "", 0, false
	}
	return p.fence.contact, p.fence.generation, true
}

// checkFence, inside tx, lets a bound store act only for its contact while
// it is confirmed, active and at the admitted generation. The root store
// passes.
func (p *PeerMessages) checkFence(ctx context.Context, tx *sql.Tx, contact string) error {
	if p.fence == nil {
		return nil
	}
	if contact != p.fence.contact {
		return ErrFenced
	}
	s, err := readContactState(ctx, tx, contact)
	if err != nil {
		return err
	}
	if !s.confirmed || s.paused || s.generation != p.fence.generation {
		return ErrFenced
	}
	return nil
}

// inTx runs fn in one transaction, after the fence check: nothing in fn may
// use p.db, which holds the only connection meanwhile.
func (p *PeerMessages) inTx(ctx context.Context, contact string, fn func(tx *sql.Tx) error) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := p.checkFence(ctx, tx, contact); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// localOnly refuses an operator's own operation on a bound store: sessions
// never queue, mark read, page or collect.
func (p *PeerMessages) localOnly() error {
	if p.fence != nil {
		return ErrFenced
	}
	return nil
}

// requireActive refuses a paused contact, inside the caller's transaction.
func requireActive(ctx context.Context, q rowQuerier, contact string) error {
	var pausedAt string
	err := q.QueryRowContext(ctx, `SELECT paused_at FROM peer_contacts WHERE identity_key = ?`, contact).Scan(&pausedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return messaging.ErrUnknownContact
	case err != nil:
		return err
	case pausedAt != "":
		return ErrPaused
	}
	return nil
}

const contactPauseSchema = `
CREATE TABLE IF NOT EXISTS contact_pause_results (
    operation_id TEXT PRIMARY KEY,
    changed      INTEGER NOT NULL,
    generation   INTEGER NOT NULL,
    paused_at    TEXT NOT NULL
);
`

// ContactPause pauses a contact (Paused) or resumes it, under the
// operator's operation ID and audit row.
type ContactPause struct {
	OperationID, Contact string
	Paused               bool
	AuditID              int64
	At                   time.Time
}

// ContactPauseResult is what an operation did: whether it changed the
// state, the generation after it, and when the contact was paused (zero
// once active).
type ContactPauseResult struct {
	Changed    bool
	Generation int64
	PausedAt   time.Time
}

// SetContactPaused pauses or resumes a confirmed contact in one
// transaction with its audit. A change moves the generation on; pausing a
// paused contact or resuming an active one changes nothing. The same
// operation ID again answers its first result, whatever happened since,
// and the same ID for another contact or action is ErrConflict.
func (s *Store) SetContactPaused(ctx context.Context, r ContactPause) (ContactPauseResult, error) {
	out, _, err := s.ApplyContactPause(ctx, r)
	return out, err
}

// ApplyContactPause is SetContactPaused that also reports whether this call
// changed the state now: a retry answers its first result, Changed
// included, but applied is false, so nothing acts on it twice.
func (s *Store) ApplyContactPause(ctx context.Context, r ContactPause) (out ContactPauseResult, applied bool, err error) {
	action := "resume"
	if r.Paused {
		action = "pause"
	}
	op := operation{r.OperationID, action, r.Contact, operationFingerprint(action, r.Contact), r.AuditID, r.At}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, false, err
	}
	defer tx.Rollback()
	if err := requireContact(ctx, tx, contactKey(r.Contact)); err != nil {
		return out, false, err
	}
	replay, err := registered(ctx, tx, op)
	if err != nil {
		return out, false, err
	}
	detail := action + " (replayed)"
	if replay {
		out, err = pauseResult(ctx, tx, r.OperationID)
	} else {
		out, detail, err = changePause(ctx, tx, r)
	}
	if err != nil {
		return out, false, err
	}
	if err := register(ctx, tx, op, replay); err != nil {
		return out, false, err
	}
	if err := finishPauseAudit(ctx, tx, r.AuditID, detail, replay); err != nil {
		return out, false, err
	}
	if err := tx.Commit(); err != nil {
		return out, false, err
	}
	return out, out.Changed && !replay, nil
}

// finishPauseAudit finishes the operation's audit row. A retry finishes
// its own row if it brought a new one, and leaves the first one, already
// finished, as it is.
func finishPauseAudit(ctx context.Context, tx *sql.Tx, auditID int64, detail string, replay bool) error {
	if !replay {
		return finishAudit(ctx, tx, auditID, AuditOutcome{Result: AuditAccepted, Detail: detail})
	}
	_, err := tx.ExecContext(ctx, `UPDATE operator_audit SET result = ?, detail = ? WHERE id = ? AND result = ?`,
		AuditAccepted, detail, auditID, AuditPending)
	return err
}

// changePause applies a new pause or resume and keeps its result for a
// retry; detail is what the audit records.
func changePause(ctx context.Context, tx *sql.Tx, r ContactPause) (ContactPauseResult, string, error) {
	var pausedAt string
	var out ContactPauseResult
	if err := tx.QueryRowContext(ctx, `SELECT paused_at, generation FROM peer_contacts WHERE identity_key = ?`, r.Contact).
		Scan(&pausedAt, &out.Generation); err != nil {
		return out, "", err
	}
	detail := pauseDetail(r.Paused, false)
	if (pausedAt != "") != r.Paused {
		out.Changed, out.Generation, detail = true, out.Generation+1, pauseDetail(r.Paused, true)
		pausedAt = ""
		if r.Paused {
			pausedAt = msgStamp(r.At)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET paused_at = ?, generation = ? WHERE identity_key = ?`,
			pausedAt, out.Generation, r.Contact); err != nil {
			return out, "", err
		}
	}
	if pausedAt != "" {
		var err error
		if out.PausedAt, err = time.Parse(time.RFC3339Nano, pausedAt); err != nil {
			return out, "", err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO contact_pause_results (operation_id, changed, generation, paused_at) VALUES (?, ?, ?, ?)`,
		r.OperationID, out.Changed, out.Generation, pausedAt)
	return out, detail, err
}

// pauseDetail is the audit's word for a pause or resume that changed the
// state, or found it so already.
func pauseDetail(paused, changed bool) string {
	switch {
	case paused && changed:
		return "pause"
	case paused:
		return "already paused"
	case changed:
		return "resume"
	}
	return "already active"
}

// pauseResult is a retried operation's first result.
func pauseResult(ctx context.Context, tx *sql.Tx, operationID string) (ContactPauseResult, error) {
	var out ContactPauseResult
	var pausedAt string
	err := tx.QueryRowContext(ctx, `SELECT changed, generation, paused_at FROM contact_pause_results WHERE operation_id = ?`, operationID).
		Scan(&out.Changed, &out.Generation, &pausedAt)
	if err != nil || pausedAt == "" {
		return out, err
	}
	out.PausedAt, err = time.Parse(time.RFC3339Nano, pausedAt)
	return out, err
}

// FenceHolds: a bound store's contact is still confirmed and active at the
// generation it was admitted under; never for the root store. A live link
// checks it between rounds to end early; each transaction still checks.
func (p *PeerMessages) FenceHolds(ctx context.Context) bool {
	if p.fence == nil {
		return false
	}
	return p.inTx(ctx, p.fence.contact, func(*sql.Tx) error { return nil }) == nil
}
