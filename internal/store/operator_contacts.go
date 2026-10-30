package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
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
	To                         []string // the contact's agents, none for its operator (#222)
	AuditID                    int64
	Now                        time.Time
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
	op := operation{c.OperationID, "send", c.Contact, operationFingerprint("send", c.Contact, append([]string{c.Text}, c.To...)...), c.AuditID, c.Now}
	key := messageKey{c.Contact, messaging.DirOut, c.OperationID}
	var row messaging.Row
	err := s.operationTx(ctx, op, func(tx *sql.Tx, replay bool) error {
		var err error
		if replay {
			var held storedRow
			held, _, err = messageRow(ctx, tx, key)
			row = held.Row
			return err
		}
		row, err = queueIn(ctx, tx, outgoing{key: key, m: messaging.Message{ID: c.OperationID, Text: c.Text, To: c.To}, now: c.Now})
		return err
	})
	if err != nil {
		return messaging.Row{}, err
	}
	return row, nil
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
	if err := requireContact(ctx, tx, contactKey(op.contact)); err != nil {
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
