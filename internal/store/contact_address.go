package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// An address change (/pair address) moves a contact to a new destination
// once a probe proved the same identity there. Each request is a durable
// operation under its message ID, bound to its contact and candidate, with
// its own operator audit row: a replay answers its stored outcome and never
// applies an old candidate again.

// contactAddressSchema keeps one row per address operation.
const contactAddressSchema = `
CREATE TABLE IF NOT EXISTS contact_address_ops (
    message_id TEXT PRIMARY KEY,
    contact    TEXT NOT NULL,
    candidate  TEXT NOT NULL,
    state      TEXT NOT NULL,
    reason     TEXT NOT NULL DEFAULT '',
    audit_id   INTEGER NOT NULL,
    at         TEXT NOT NULL
);
`

// Address operation states.
const (
	AddressRunning  = "running"
	AddressAccepted = "accepted"
	AddressRefused  = "refused"
)

var (
	// ErrAddressOpConflict is a message ID already used for another
	// contact or candidate.
	ErrAddressOpConflict = errors.New("message_id was used for another address change")
	// ErrAddressOpSettled is an operation that is no longer running.
	ErrAddressOpSettled = errors.New("address change already settled")
)

// AddressOp is one requested address change.
type AddressOp struct {
	ID, Contact, Candidate string
	At                     time.Time
	PrincipalUID           int
}

// AddressOutcome is where an address operation stands; Reason is a fixed
// text for a refused one.
type AddressOutcome struct {
	State, Reason string
}

// BeginContactAddress records a new operation as running with its pending
// audit row, in one transaction. A known ID for the same contact and
// candidate answers its stored outcome, writing nothing; for another, it is
// ErrAddressOpConflict.
func (s *Store) BeginContactAddress(ctx context.Context, op AddressOp) (AddressOutcome, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AddressOutcome{}, false, err
	}
	defer tx.Rollback()
	known, found, err := getAddressOp(ctx, tx, op.ID)
	if err != nil || found {
		return addressReplay(known, found, op, err)
	}
	// A paused contact doesn't move; a move begun at this generation can't
	// commit at another (#276).
	if err := requireActive(ctx, tx, op.Contact); err != nil {
		return AddressOutcome{}, false, err
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM peer_contacts WHERE identity_key = ?`, op.Contact).Scan(&generation); err != nil {
		return AddressOutcome{}, false, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO operator_audit (ts, action, principal_uid, message_id, scope, result) VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now().UTC(), "contact address", op.PrincipalUID, op.ID, op.Contact, AuditPending)
	if err != nil {
		return AddressOutcome{}, false, err
	}
	auditID, err := res.LastInsertId()
	if err != nil {
		return AddressOutcome{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO contact_address_ops (message_id, contact, candidate, state, audit_id, at, generation)
VALUES (?, ?, ?, ?, ?, ?, ?)`, op.ID, op.Contact, op.Candidate, AddressRunning, auditID, stamp(op.At), generation)
	if err != nil {
		return AddressOutcome{}, false, err
	}
	return AddressOutcome{State: AddressRunning}, false, tx.Commit()
}

// addressReplay answers a known operation: its outcome, when it is the same
// request.
func addressReplay(known addressOp, found bool, op AddressOp, err error) (AddressOutcome, bool, error) {
	switch {
	case err != nil:
		return AddressOutcome{}, false, err
	case known.contact != op.Contact || known.candidate != op.Candidate:
		return AddressOutcome{}, false, ErrAddressOpConflict
	}
	return known.outcome, found, nil
}

// addressOp is an operation as stored.
type addressOp struct {
	id, contact, candidate string
	outcome                AddressOutcome
	auditID                int64
	generation             int64 // the contact's when the operation began (#276)
}

// running: the operation is still waiting for its outcome.
func (op addressOp) running() bool { return op.outcome.State == AddressRunning }

func getAddressOp(ctx context.Context, q rowQuerier, id string) (addressOp, bool, error) {
	op := addressOp{id: id}
	err := q.QueryRowContext(ctx, `SELECT contact, candidate, state, reason, audit_id, generation FROM contact_address_ops WHERE message_id = ?`, id).
		Scan(&op.contact, &op.candidate, &op.outcome.State, &op.outcome.Reason, &op.auditID, &op.generation)
	if errors.Is(err, sql.ErrNoRows) {
		return addressOp{}, false, nil
	}
	return op, err == nil, err
}

// ContactAddress is a verified address for operation OpID: the contact
// moves from From (as of FromAt) to To, while this side still dials it.
type ContactAddress struct {
	OpID, Contact, From string
	FromAt              time.Time
	To                  string
	At                  time.Time
	StillDials          func(peer.Contact) bool
}

// ChangeContactAddress commits a verified address in one transaction: only
// for its own running operation, only while the contact still has From as
// of FromAt and this side still dials it. A destination changed meanwhile or
// a side that no longer dials settles the operation as refused, committed,
// before the error returns. The new destination is stamped strictly after
// FromAt, whatever the clock says; the rest of the contact stays.
func (s *Store) ChangeContactAddress(ctx context.Context, a ContactAddress) (peer.Contact, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return peer.Contact{}, err
	}
	defer tx.Rollback()
	op, err := runningOp(ctx, tx, a)
	if err != nil {
		return peer.Contact{}, err
	}
	c, found, err := getContact(ctx, tx, a.Contact)
	if err != nil || !found {
		return peer.Contact{}, errors.Join(err, notFound(found))
	}
	if refusal := addressRefusal(c, a, op.generation); refusal != nil {
		if err := settleRefused(ctx, tx, op, refusal); err != nil {
			return peer.Contact{}, err
		}
		return peer.Contact{}, errors.Join(tx.Commit(), refusal)
	}
	moved := c
	moved.Destination, moved.DestinationAt = a.To, later(a.At, a.FromAt)
	if err := writeAddress(ctx, tx, contactMove{c, moved, a.OpID}, op.auditID); err != nil {
		return peer.Contact{}, err
	}
	return moved, tx.Commit()
}

// runningOp is a's operation, which must still run for a's contact and
// candidate.
func runningOp(ctx context.Context, tx *sql.Tx, a ContactAddress) (addressOp, error) {
	op, found, err := getAddressOp(ctx, tx, a.OpID)
	switch {
	case err != nil:
		return addressOp{}, err
	case !found || op.contact != a.Contact || op.candidate != a.To:
		return addressOp{}, ErrAddressOpConflict
	case !op.running():
		return addressOp{}, ErrAddressOpSettled
	}
	return op, nil
}

// addressRefusal is why the contact can't move now, if it can't.
func addressRefusal(c peer.Contact, a ContactAddress, generation int64) error {
	if c.Generation != generation || !c.PausedAt.IsZero() {
		return ErrFenced // paused or resumed since the move began (#276)
	}
	if c.Destination != a.From || !c.DestinationAt.Equal(a.FromAt) {
		return peer.ErrDestinationChanged
	}
	if a.StillDials != nil && !a.StillDials(c) {
		return peer.ErrNotDialing
	}
	return nil
}

// later is at, or the instant after from when the clock says otherwise.
func later(at, from time.Time) time.Time {
	if next := from.Add(time.Nanosecond); !at.After(from) {
		return next
	}
	return at
}

// writeAddress stores the move, its audit, and the operation accepted with
// its own audit, inside tx.
func writeAddress(ctx context.Context, tx *sql.Tx, m contactMove, auditID int64) error {
	c := m.after
	if _, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET destination = ?, destination_at = ? WHERE identity_key = ?`,
		c.Destination, destinationStamp(c.DestinationAt), c.IdentityKey); err != nil {
		return fmt.Errorf("store contact address: %w", err)
	}
	if err := auditMove(ctx, tx, m); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE contact_address_ops SET state = ? WHERE message_id = ?`, AddressAccepted, m.opID); err != nil {
		return err
	}
	return finishAudit(ctx, tx, auditID, AuditOutcome{Result: AuditAccepted, Detail: m.before.Destination + " -> " + c.Destination})
}

// RefuseContactAddress settles a running operation as refused with a fixed
// reason, and its audit as rejected; a settled one stays as it is.
func (s *Store) RefuseContactAddress(ctx context.Context, opID, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	op, found, err := getAddressOp(ctx, tx, opID)
	if err != nil || !found {
		return err
	}
	if !op.running() {
		return nil
	}
	if err := settleRefused(ctx, tx, op, errors.New(reason)); err != nil {
		return err
	}
	return tx.Commit()
}

// settleRefused marks an operation refused and finishes its audit, inside
// tx; the caller commits.
func settleRefused(ctx context.Context, tx *sql.Tx, op addressOp, why error) error {
	if _, err := tx.ExecContext(ctx, `UPDATE contact_address_ops SET state = ?, reason = ? WHERE message_id = ?`, AddressRefused, why.Error(), op.id); err != nil {
		return err
	}
	return finishAudit(ctx, tx, op.auditID, AuditOutcome{Result: AuditRejected, Detail: why.Error()})
}
