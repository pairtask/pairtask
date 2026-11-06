package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// operatorContactSchema remembers every operator contact operation by the
// operation's own message ID (#210), with a fingerprint of what it asked,
// so a retry returns what the first one did and an ID reused for anything
// else conflicts, across restarts.
const operatorContactSchema = `
CREATE TABLE IF NOT EXISTS operator_contact_requests (
    operation_id TEXT PRIMARY KEY,
    op           TEXT NOT NULL,
    contact      TEXT NOT NULL,
    fingerprint  BLOB NOT NULL,
    created_at   TEXT NOT NULL
);
`

// ContactSend is one operator send: its operation ID names the message.
type ContactSend struct {
	OperationID, Contact, Text string
	To                         []string             // the contact's agents, none for its operator (#222)
	Review                     *messaging.ReviewAsk // a review request for the one agent in To (#235)
	AuditID                    int64
	Now                        time.Time
	// Here are this side's agents the send tags, and OperatorHere whether it
	// tags this side's operator (#278); Seq is the order its message here
	// takes, 0 for the next. Recipients are the tagged agents as the hub
	// resolved them, with the session each is bound to, if any.
	Here         []string
	OperatorHere bool
	Seq          uint64
	Recipients   []protocol.Recipient
}

// ContactRead is one operator read of messages shown to the operator.
type ContactRead struct {
	OperationID, Contact string
	IDs                  []string
	AuditID              int64
	Now                  time.Time
}

// operation is one registered operator operation: what it asked, the
// audit row it finishes and when.
type operation struct {
	id, op, contact string
	fingerprint     []byte
	auditID         int64
	now             time.Time
}

// operationFingerprint covers the operation, the contact and what it
// carries, each length-prefixed.
func operationFingerprint(op, contact string, parts ...string) []byte {
	h := sha256.New()
	for _, part := range append([]string{op, contact}, parts...) {
		binary.Write(h, binary.BigEndian, uint64(len(part)))
		h.Write([]byte(part))
	}
	return h.Sum(nil)
}

// canonicalIDs is a read's IDs as a sorted set.
func canonicalIDs(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// OperatorContactSend queues the operator's message and finishes its audit
// as accepted in one transaction. A retry of the same operation returns the
// message as first queued (its ID, sequence and time; collected text stays
// collected); the operation ID reused for anything else is ErrConflict.
func (s *Store) OperatorContactSend(ctx context.Context, c ContactSend) (messaging.Row, error) {
	sent, err := s.contactSend(ctx, c)
	return sent.Row, err
}

// ContactSent is what a ⇄ send left: its row there, the advisories read
// before it was queued, and whether it was the same send again, already
// kept, with nothing new here to deliver or announce (#278).
type ContactSent struct {
	Row      messaging.Row
	Advice   []protocol.ReviewAdvisory
	Replayed bool
}

// OperatorContactSent is OperatorContactSend, saying whether it was a replay.
func (s *Store) OperatorContactSent(ctx context.Context, c ContactSend) (ContactSent, error) {
	return s.contactSend(ctx, c)
}

// OperatorReviewAsk is OperatorContactSend for a review request: queued
// the same, with the advisories read where it was queued, before it was
// written (#304); none for a replay.
func (s *Store) OperatorReviewAsk(ctx context.Context, c ContactSend) (messaging.Row, []protocol.ReviewAdvisory, error) {
	sent, err := s.contactSend(ctx, c)
	return sent.Row, sent.Advice, err
}

func (s *Store) contactSend(ctx context.Context, c ContactSend) (ContactSent, error) {
	op := operation{c.OperationID, "send", c.Contact, c.fingerprint(), c.AuditID, c.Now}
	key := messageKey{c.Contact, messaging.DirOut, c.OperationID}
	var sent ContactSent
	err := s.operationTx(ctx, op, func(tx *sql.Tx, replay bool) error {
		var err error
		if sent.Replayed = replay; replay {
			var held storedRow
			held, _, err = messageRow(ctx, tx, key)
			sent.Row = held.Row
			return err
		}
		m := messaging.Message{ID: c.OperationID, Text: c.Text, To: c.To, Review: c.Review}
		if !validOutgoing(m) {
			return fmt.Errorf("%w: ID, text, routing or review request", messaging.ErrInvalid)
		}
		if sent.Advice, err = askAdvice(ctx, tx, c.Contact, m); err != nil {
			return err
		}
		if sent.Row, err = queueIn(ctx, tx, outgoing{key: key, m: m, now: c.Now}); err != nil {
			return err
		}
		return shareHere(ctx, tx, c, m)
	})
	if err != nil {
		return ContactSent{}, err
	}
	return sent, nil
}

// fingerprint is what the send's operation ID stands for. A send tagging
// here is fingerprinted apart, its lists counted, so no name can move
// between them unnoticed; any other send keeps the fingerprint it always had.
func (c ContactSend) fingerprint() []byte {
	review := reviewAskJSON(c.Review)
	here := c.homeTargets()
	if len(here) == 0 {
		parts := append([]string{c.Text}, c.To...)
		if review.Valid {
			parts = append(parts, "review", review.String)
		}
		return operationFingerprint("send", c.Contact, parts...)
	}
	parts := append([]string{c.Text, review.String, strconv.Itoa(len(c.To))}, c.To...)
	parts = append(append(parts, strconv.Itoa(len(here))), here...)
	return operationFingerprint("send here", c.Contact, parts...)
}

// askAdvice is a review request's advisories, read before it is queued;
// none for any other message.
func askAdvice(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) ([]protocol.ReviewAdvisory, error) {
	if m.Review == nil || len(m.To) != 1 {
		return nil, nil
	}
	return ReviewAdvisories(ctx, tx, contact, m.To[0])
}

// OperatorContactRead marks the shown messages read and finishes its audit
// as accepted in one transaction; it counts the messages newly marked, so
// a retry of the same operation marks none.
func (s *Store) OperatorContactRead(ctx context.Context, c ContactRead) (int, error) {
	ids := canonicalIDs(c.IDs)
	op := operation{c.OperationID, "read", c.Contact, operationFingerprint("read", c.Contact, ids...), c.AuditID, c.Now}
	marked := 0
	err := s.operationTx(ctx, op, func(tx *sql.Tx, replay bool) error {
		if replay {
			return nil
		}
		var err error
		marked, err = markReadIn(ctx, tx, readSet{c.Contact, ids, c.Now})
		return err
	})
	if err != nil {
		return 0, err
	}
	return marked, nil
}

// operationTx runs one operator operation: the registry decides whether it
// is new or a retry, apply makes the change (or reads the first result),
// and the audit row is finished as accepted, all in one transaction.
func (s *Store) operationTx(ctx context.Context, op operation, apply func(tx *sql.Tx, replay bool) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// An unpaired contact's history stays readable; a send is refused
	// where it is queued (#276).
	if err := requireKnown(ctx, tx, contactKey(op.contact)); err != nil {
		return err
	}
	replay, err := registered(ctx, tx, op)
	if err != nil {
		return err
	}
	if err := apply(tx, replay); err != nil {
		return err
	}
	if err := register(ctx, tx, op, replay); err != nil {
		return err
	}
	if err := finishAudit(ctx, tx, op.auditID, AuditOutcome{Result: AuditAccepted}); err != nil {
		return err
	}
	return tx.Commit()
}

// registered reports an operation already done; the same ID for anything
// else is a conflict.
func registered(ctx context.Context, tx *sql.Tx, op operation) (bool, error) {
	var held []byte
	err := tx.QueryRowContext(ctx, `SELECT fingerprint FROM operator_contact_requests WHERE operation_id = ?`, op.id).Scan(&held)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	case !bytes.Equal(held, op.fingerprint):
		return false, messaging.ErrConflict
	}
	return true, nil
}

func register(ctx context.Context, tx *sql.Tx, op operation, replay bool) error {
	if replay {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO operator_contact_requests (operation_id, op, contact, fingerprint, created_at)
VALUES (?, ?, ?, ?, ?)`, op.id, op.op, op.contact, op.fingerprint, msgStamp(op.now))
	return err
}
