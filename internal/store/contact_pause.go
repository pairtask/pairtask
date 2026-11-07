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

// sessionAuthority is what a session is admitted under, and what each of
// its transactions compares (#276): the contact's generation, its pairing
// epoch and its pairing ID ("" for a legacy contact).
type sessionAuthority struct {
	generation, epoch int64
	pairingID         string
}

// linkFence is the contact and authority a session was admitted under.
type linkFence struct {
	contact string
	sessionAuthority
}

// contactState is what admission and the fence check: whether the contact
// was confirmed, whether it is paused or unpaired, here or by the other
// side (unpairedByThem), and its authority.
type contactState struct {
	confirmed, paused, unpaired bool
	unpairedByThem              bool
	sessionAuthority
}

func readContactState(ctx context.Context, q rowQuerier, contact string) (contactState, error) {
	known, err := knownContact(ctx, q, contactKey(contact))
	if err != nil || !known {
		return contactState{}, err
	}
	s := contactState{confirmed: true}
	var pausedAt, unpairedAt, peerState string
	var id []byte
	err = q.QueryRowContext(ctx, `SELECT paused_at, unpaired_at, peer_state, generation, pairing_epoch, pairing_id FROM peer_contacts WHERE identity_key = ?`, contact).
		Scan(&pausedAt, &unpairedAt, &peerState, &s.generation, &s.epoch, &id)
	s.paused, s.unpaired, s.pairingID = pausedAt != "", unpairedAt != "", string(id)
	s.unpairedByThem = peerState == remoteUnpaired
	return s, err
}

// Admit binds a session to a confirmed, active contact at its generation,
// pairing epoch and pairing ID.
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
	case s.unpaired:
		return nil, ErrUnpaired
	case s.unpairedByThem:
		return nil, ErrUnpairedByThem
	case s.paused:
		return nil, ErrPaused
	}
	return &PeerMessages{db: p.db, fence: &linkFence{contact: contact, sessionAuthority: s.sessionAuthority}, gate: p.gate}, nil
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
// it is confirmed, active and at the admitted generation, epoch and
// pairing ID. The root store passes.
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
	if !s.admits(p.fence.sessionAuthority) {
		return ErrFenced
	}
	return nil
}

// admits: a session admitted under authority may still act, the contact
// confirmed, active, not paused or resumed since, and still the same
// pairing.
func (s contactState) admits(authority sessionAuthority) bool {
	return s.active() && s.sessionAuthority == authority
}

func (s contactState) active() bool {
	return s.confirmed && !s.paused && !s.ended()
}

// ended: the pairing was ended, here or by the other side.
func (s contactState) ended() bool { return s.unpaired || s.unpairedByThem }

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

// contactFlags are what the refusals below read of a contact.
type contactFlags struct {
	pausedAt, unpairedAt, peerState string
}

// readFlags reads a contact's flags inside the caller's transaction; found
// is false for no contact row.
func readFlags(ctx context.Context, q rowQuerier, contact string) (contactFlags, bool, error) {
	var f contactFlags
	err := q.QueryRowContext(ctx, `SELECT paused_at, unpaired_at, peer_state FROM peer_contacts WHERE identity_key = ?`,
		contact).Scan(&f.pausedAt, &f.unpairedAt, &f.peerState)
	if errors.Is(err, sql.ErrNoRows) {
		return contactFlags{}, false, nil
	}
	return f, err == nil, err
}

// requireActive refuses a paused or unpaired contact, inside the caller's
// transaction.
func requireActive(ctx context.Context, q rowQuerier, contact string) error {
	f, found, err := readFlags(ctx, q, contact)
	switch {
	case err != nil:
		return err
	case !found:
		return messaging.ErrUnknownContact
	case f.unpairedAt != "":
		return ErrUnpaired
	case f.pausedAt != "":
		return ErrPaused
	}
	return nil
}

// refuseUnpairedByThem refuses a contact the other side unpaired, inside
// the caller's transaction; any other, or none, passes.
func refuseUnpairedByThem(ctx context.Context, q rowQuerier, contact string) error {
	f, _, err := readFlags(ctx, q, contact)
	if err == nil && f.peerState == remoteUnpaired {
		return ErrUnpairedByThem
	}
	return err
}

// refuseUnpaired refuses an unpaired contact, inside the caller's
// transaction; any other, or none, passes.
func refuseUnpaired(ctx context.Context, q rowQuerier, contact string) error {
	f, _, err := readFlags(ctx, q, contact)
	if err == nil && f.unpairedAt != "" {
		return ErrUnpaired
	}
	return err
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
func (s *Store) ApplyContactPause(ctx context.Context, r ContactPause) (ContactPauseResult, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContactPauseResult{}, false, err
	}
	defer tx.Rollback()
	out, replay, err := pauseIn(ctx, tx, r)
	if err != nil {
		return out, false, err
	}
	if err := tx.Commit(); err != nil {
		return out, false, err
	}
	return out, out.Changed && !replay, nil
}

// pauseIn is ApplyContactPause inside its transaction: the first result
// for a retry, else the change, then the registry and the audit.
func pauseIn(ctx context.Context, tx *sql.Tx, r ContactPause) (ContactPauseResult, bool, error) {
	action := pauseDetail(r.Paused, true)
	op := operation{r.OperationID, action, r.Contact, operationFingerprint(action, r.Contact), r.AuditID, r.At}
	if err := requireKnown(ctx, tx, contactKey(r.Contact)); err != nil {
		return ContactPauseResult{}, false, err
	}
	replay, err := registered(ctx, tx, op)
	if err != nil {
		return ContactPauseResult{}, false, err
	}
	out, detail, err := pauseOutcome(ctx, tx, r, replay)
	if err != nil {
		return out, replay, err
	}
	if err := register(ctx, tx, op, replay); err != nil {
		return out, replay, err
	}
	return out, replay, finishPauseAudit(ctx, tx, pauseAudit{r.AuditID, detail, replay})
}

// pauseOutcome is a retry's first result, or the change made now, with
// what the audit records.
func pauseOutcome(ctx context.Context, tx *sql.Tx, r ContactPause, replay bool) (ContactPauseResult, string, error) {
	if !replay {
		if err := refuseUnpaired(ctx, tx, r.Contact); err != nil {
			return ContactPauseResult{}, "", err
		}
		return changePause(ctx, tx, r)
	}
	out, err := pauseResult(ctx, tx, r.OperationID)
	return out, pauseDetail(r.Paused, true) + " (replayed)", err
}

// pauseAudit is the audit row an operation finishes, what it records, and
// whether the operation is a retry.
type pauseAudit struct {
	id     int64
	detail string
	replay bool
}

// finishPauseAudit finishes the operation's audit row. A retry finishes
// its own row if it brought a new one, and leaves the first one, already
// finished, as it is.
func finishPauseAudit(ctx context.Context, tx *sql.Tx, a pauseAudit) error {
	if !a.replay {
		return finishAudit(ctx, tx, a.id, AuditOutcome{Result: AuditAccepted, Detail: a.detail})
	}
	_, err := tx.ExecContext(ctx, `UPDATE operator_audit SET result = ?, detail = ? WHERE id = ? AND result = ?`,
		AuditAccepted, a.detail, a.id, AuditPending)
	return err
}

// changePause applies a new pause or resume and keeps its result for a
// retry; detail is what the audit records.
func changePause(ctx context.Context, tx *sql.Tx, r ContactPause) (ContactPauseResult, string, error) {
	var pausedAt string
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT paused_at, generation FROM peer_contacts WHERE identity_key = ?`, r.Contact).
		Scan(&pausedAt, &generation); err != nil {
		return ContactPauseResult{}, "", err
	}
	changed := (pausedAt != "") != r.Paused
	if changed {
		generation++
		pausedAt = pausedStamp(r)
		if err := applyPause(ctx, tx, r, generation); err != nil {
			return ContactPauseResult{}, "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO contact_pause_results (operation_id, changed, generation, paused_at) VALUES (?, ?, ?, ?)`,
		r.OperationID, changed, generation, pausedAt); err != nil {
		return ContactPauseResult{}, "", err
	}
	out, err := pauseResultOf(changed, generation, pausedAt)
	return out, pauseDetail(r.Paused, changed), err
}

// applyPause writes a pause or resume at its generation; a pause also ends
// the contact's session consent (#293).
func applyPause(ctx context.Context, tx *sql.Tx, r ContactPause, generation int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET paused_at = ?, generation = ? WHERE identity_key = ?`,
		pausedStamp(r), generation, r.Contact); err != nil {
		return err
	}
	if !r.Paused {
		return nil
	}
	return pauseSessions(ctx, tx, r.Contact, r.At)
}

// pausedStamp is paused_at as a change to r writes it: "" for a resume.
func pausedStamp(r ContactPause) string {
	if !r.Paused {
		return ""
	}
	return msgStamp(r.At)
}

// pauseResultOf is a result from its stored columns.
func pauseResultOf(changed bool, generation int64, pausedAt string) (ContactPauseResult, error) {
	out := ContactPauseResult{Changed: changed, Generation: generation}
	if pausedAt == "" {
		return out, nil
	}
	var err error
	out.PausedAt, err = time.Parse(time.RFC3339Nano, pausedAt)
	return out, err
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
	var changed bool
	var generation int64
	var pausedAt string
	err := tx.QueryRowContext(ctx, `SELECT changed, generation, paused_at FROM contact_pause_results WHERE operation_id = ?`, operationID).
		Scan(&changed, &generation, &pausedAt)
	if err != nil {
		return ContactPauseResult{}, err
	}
	return pauseResultOf(changed, generation, pausedAt)
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
