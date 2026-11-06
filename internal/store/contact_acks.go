package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// The operator's acknowledgment of a contact's messages (#287): explicit
// and private. Showing a message still marks it read and sends the contact
// its receipt, as before; acknowledging changes neither, and sends nothing.
// Messages received before acknowledgments were tracked stay "before",
// below a floor written once at migration, never taken for acknowledged.

// peerAckFloorsColumns is peer_ack_floors with pairing_epoch in its key (#276).
const peerAckFloorsColumns = `(
    contact TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    floor   INTEGER NOT NULL DEFAULT 0,
    expired INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (contact, pairing_epoch)
)`

// ackFloor is a row's contact's floor: 0 for a contact with none.
const ackFloor = `COALESCE((SELECT f.floor FROM peer_ack_floors f WHERE f.contact = peer_messages.contact AND f.pairing_epoch = peer_messages.pairing_epoch), 0)`

// ackEligible: a message received for this side's operator, after the floor.
// Messages routed to agents are theirs to acknowledge.
const ackEligible = `direction = 'in' AND state IN ('received', 'read', 'refused') AND cursor > ` + ackFloor

// needsAck: eligible, and neither acknowledged nor expired.
const needsAck = ackEligible + ` AND acked_at IS NULL AND ack_expired_at IS NULL`

// ackStateColumn is a row's acknowledgment state, "" when it has none.
const ackStateColumn = `CASE WHEN direction != 'in' OR state NOT IN ('received', 'read', 'refused') THEN ''
    WHEN cursor <= ` + ackFloor + ` THEN 'before'
    WHEN ack_expired_at IS NOT NULL THEN 'expired'
    WHEN acked_at IS NOT NULL THEN 'acked'
    ELSE 'needs_ack' END`

// seedAckFloors puts every existing contact's received messages below its
// floor, once, when acknowledgments start being tracked. It runs inside the
// pairing-epoch transition (#276), where both tables have the epoch.
func seedAckFloors(tx *sql.Tx) error {
	_, err := tx.Exec(`INSERT INTO peer_ack_floors (contact, pairing_epoch, floor)
SELECT contact, pairing_epoch, MAX(cursor) FROM peer_messages WHERE direction = 'in' GROUP BY contact, pairing_epoch
ON CONFLICT (contact, pairing_epoch) DO NOTHING`)
	return err
}

// AckSelector is what one acknowledgment covers: the IDs, or every
// message up to a cursor.
type AckSelector struct {
	IDs  []string
	Upto int64
}

// ErrAckBound refuses a cursor past the contact's newest received message,
// which would reach messages not yet received.
var ErrAckBound = errors.New("contact_ack_upto is past the newest message received")

// Ack acknowledges the selected messages that wait for it, and returns how
// many it acknowledged and where the contact's acknowledgments stand.
// Expired and earlier ones are left as they are.
func (p *PeerMessages) Ack(ctx context.Context, contact string, sel AckSelector, at time.Time) (int, protocol.ContactAckSummary, error) {
	if err := p.localOnly(); err != nil {
		return 0, protocol.ContactAckSummary{}, err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, protocol.ContactAckSummary{}, err
	}
	defer tx.Rollback()
	acked, err := ackIn(ctx, tx, ackRequest{contact, sel, at})
	if err != nil {
		return 0, protocol.ContactAckSummary{}, err
	}
	summary, err := ackSummary(ctx, tx, contact)
	if err != nil {
		return 0, protocol.ContactAckSummary{}, err
	}
	return acked, summary, tx.Commit()
}

// ackRequest is one acknowledgment to apply: whose messages, which, and when.
type ackRequest struct {
	contact string
	sel     AckSelector
	at      time.Time
}

func ackIn(ctx context.Context, tx *sql.Tx, r ackRequest) (int, error) {
	// An unpaired contact's history is acknowledged to nobody (#276).
	if err := refuseUnpaired(ctx, tx, r.contact); err != nil {
		return 0, err
	}
	where, args, err := ackSelection(ctx, tx, r.contact, r.sel)
	if err != nil {
		return 0, err
	}
	// Only the current pairing's messages: an ID an earlier one used, or a
	// cursor reaching its rows, acknowledges nothing there (#276).
	res, err := tx.ExecContext(ctx, `UPDATE peer_messages SET acked_at = ? WHERE contact = ? AND pairing_epoch = `+currentEpoch+`
    AND `+needsAck+` AND `+where, append([]any{msgStamp(r.at), r.contact, r.contact}, args...)...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// ackSelection is the selector as a condition, after checking a cursor
// against the newest message received.
func ackSelection(ctx context.Context, tx *sql.Tx, contact string, sel AckSelector) (string, []any, error) {
	if sel.Upto == 0 {
		args := make([]any, len(sel.IDs))
		for i, id := range sel.IDs {
			args[i] = id
		}
		return `message_id IN (?` + strings.Repeat(`, ?`, len(sel.IDs)-1) + `)`, args, nil
	}
	newest, err := newestReceived(ctx, tx, contact)
	if err != nil {
		return "", nil, err
	}
	if sel.Upto > newest {
		return "", nil, ErrAckBound
	}
	return `cursor <= ?`, []any{sel.Upto}, nil
}

// AckSummary is where a contact's acknowledgments stand.
func (p *PeerMessages) AckSummary(ctx context.Context, contact string) (protocol.ContactAckSummary, error) {
	if err := p.localOnly(); err != nil {
		return protocol.ContactAckSummary{}, err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.ContactAckSummary{}, err
	}
	defer tx.Rollback()
	return ackSummary(ctx, tx, contact)
}

// ackSummary reads the count and the cursor bound in one statement, so an
// "everything" acknowledgment covers exactly what was counted. It is the
// contact's current pairing's: an earlier one's are history (#276).
func ackSummary(ctx context.Context, q rowQuerier, contact string) (protocol.ContactAckSummary, error) {
	var s protocol.ContactAckSummary
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+needsAck+`), 0), COALESCE(MAX(CASE WHEN `+needsAck+` THEN cursor END), 0),
    COALESCE((SELECT expired FROM peer_ack_floors WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+`), 0)
FROM peer_messages WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1, contact).Scan(&s.Unacked, &s.AckUpto, &s.AckExpired)
	return s, err
}

// expireUnacked expires the oldest messages waiting for acknowledgment past
// UnackedKept, counting them for good: they are never acknowledged, and
// the count outlives them. Only the current pairing's count (#276).
func expireUnacked(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE peer_messages SET ack_expired_at = ?1 WHERE cursor IN (
    SELECT cursor FROM peer_messages WHERE contact = ?2 AND pairing_epoch = `+currentEpochAt(2)+` AND `+needsAck+`
    ORDER BY cursor DESC LIMIT -1 OFFSET ?3)`,
		msgStamp(now), contact, protocol.UnackedKept)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO peer_ack_floors (contact, pairing_epoch, expired) VALUES (?, `+currentEpoch+`, ?)
ON CONFLICT(contact, pairing_epoch) DO UPDATE SET expired = expired + excluded.expired`, contact, contact, n)
	return err
}

// ContactAck is one operator acknowledgment, audited.
type ContactAck struct {
	Epoch                int64
	OperationID, Contact string
	Selector             AckSelector
	AuditID              int64
	Now                  time.Time
}

// OperatorContactAck acknowledges the selected messages and finishes its
// audit as accepted in one transaction. A retry of the same operation
// acknowledges none, and both answer where the contact stands now.
func (s *Store) OperatorContactAck(ctx context.Context, c ContactAck) (int, protocol.ContactAckSummary, error) {
	sel := AckSelector{IDs: canonicalIDs(c.Selector.IDs), Upto: c.Selector.Upto}
	op := operation{c.OperationID, "ack", c.Contact, ackFingerprint(c.Contact, sel), c.AuditID, c.Now}
	acked := 0
	var summary protocol.ContactAckSummary
	err := s.operationTx(ctx, op, func(tx *sql.Tx, replay bool) error {
		var err error
		if !replay {
			if acked, err = ackIn(ctx, tx, ackRequest{c.Contact, sel, c.Now}); err != nil {
				return err
			}
		}
		summary, err = ackSummary(ctx, tx, c.Contact)
		return err
	})
	return acked, summary, err
}

// ackFingerprint names an acknowledgment by what it selects.
func ackFingerprint(contact string, sel AckSelector) []byte {
	if sel.Upto != 0 {
		return operationFingerprint("ack", contact, "upto", strconv.FormatInt(sel.Upto, 10))
	}
	return operationFingerprint("ack", contact, append([]string{"ids"}, sel.IDs...)...)
}

// AckSummaries are every contact's acknowledgments, by identity key; a
// contact without messages has none.
func (p *PeerMessages) AckSummaries(ctx context.Context) (map[string]protocol.ContactAckSummary, error) {
	if err := p.localOnly(); err != nil {
		return nil, err
	}
	// Every contact with messages or a floor: an expired count outlives
	// the messages it counted. Each counts its current pairing (#276).
	rows, err := p.db.QueryContext(ctx, `SELECT k.contact,
    COALESCE((SELECT SUM(`+needsAck+`) FROM peer_messages WHERE contact = k.contact AND pairing_epoch = k.epoch), 0),
    COALESCE((SELECT MAX(cursor) FROM peer_messages WHERE contact = k.contact AND pairing_epoch = k.epoch AND `+needsAck+`), 0),
    COALESCE((SELECT expired FROM peer_ack_floors f WHERE f.contact = k.contact AND f.pairing_epoch = k.epoch), 0)
FROM (SELECT u.contact, COALESCE((SELECT ce.pairing_epoch FROM peer_contacts ce WHERE ce.identity_key = u.contact), 1) AS epoch
    FROM (SELECT contact FROM peer_messages UNION SELECT contact FROM peer_ack_floors) u) k`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]protocol.ContactAckSummary{}
	for rows.Next() {
		var contact string
		var s protocol.ContactAckSummary
		if err := rows.Scan(&contact, &s.Unacked, &s.AckUpto, &s.AckExpired); err != nil {
			return nil, err
		}
		out[contact] = s
	}
	return out, rows.Err()
}

// NewestReceived is the cursor of the contact's newest received message, 0
// for none.
func (p *PeerMessages) NewestReceived(ctx context.Context, contact string) (int64, error) {
	if err := p.localOnly(); err != nil {
		return 0, err
	}
	return newestReceived(ctx, p.db, contact)
}

// newestReceived is the cursor of the contact's newest message received
// in its current pairing, 0 for none.
func newestReceived(ctx context.Context, q rowQuerier, contact string) (int64, error) {
	var newest int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(cursor), 0) FROM peer_messages WHERE `+currentInbound, contact).Scan(&newest)
	return newest, err
}

// ContactOperationKnown: an operator operation with this ID was stored
// already, so a retry is answered from it, whatever holds now.
func (s *Store) ContactOperationKnown(ctx context.Context, id string) (bool, error) {
	var known bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM operator_contact_requests WHERE operation_id = ?)`, id).Scan(&known)
	return known, err
}
