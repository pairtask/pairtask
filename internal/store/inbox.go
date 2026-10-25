package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// OperatorInboxSession is internal bookkeeping for operator-inbox rows. It is
// not an authenticated session and never represents a connected human.
const OperatorInboxSession = "operator-inbox"

// Operator inbox quotas. Every message counts, including replies; only the
// operator's explicit acknowledgment ends pending.
const (
	OperatorSenderMessages = 16
	OperatorSenderBytes    = 512 << 10
	OperatorInboxMessages  = 256
	OperatorInboxBytes     = 8 << 20
)

// pageEnvelopeReserve keeps the page frame below the wire limit after framing.
const pageEnvelopeReserve = 4 << 10

var (
	ErrInvalidReply  = errors.New("in_reply_to must name an operator message delivered to this sender")
	ErrInvalidCursor = errors.New("invalid cursor; start a new scan with after_seq 0 and upper_seq 0")
	ErrInvalidLimit  = errors.New("invalid limit; use 1 to 32, or omit it for 16")
	// ErrInvalidSelection rejects an acknowledgment page without an explicit
	// upper bound above its start, or with a limit outside 1 to 256.
	ErrInvalidSelection = errors.New("invalid selection; upper_seq must exceed after_seq and limit must be 1 to 256, or omitted for 64")
)

func checkOperatorInboxCapacity(ctx context.Context, tx *sql.Tx, size durableSize) error {
	pending := `SELECT COUNT(*), COALESCE(SUM(d.wire_bytes),0) FROM durable_inbox i JOIN durable_messages d USING(message_id) JOIN message_metadata mm USING(message_id) JOIN messages m USING(seq) WHERE i.agent_id = ? AND i.acknowledged_at IS NULL`
	var count, bytes int
	if err := tx.QueryRowContext(ctx, pending+` AND m.sender = ?`, protocol.OperatorID, size.sender).Scan(&count, &bytes); err != nil {
		return err
	}
	if count >= OperatorSenderMessages {
		return inboxFull("operator sender messages", count, OperatorSenderMessages, "unacknowledged messages")
	}
	if bytes+size.bytes > OperatorSenderBytes {
		return inboxFull("operator sender bytes", bytes+size.bytes, OperatorSenderBytes, "unacknowledged bytes")
	}
	if err := tx.QueryRowContext(ctx, pending, protocol.OperatorID).Scan(&count, &bytes); err != nil {
		return err
	}
	if count >= OperatorInboxMessages {
		return inboxFull("operator inbox messages", count, OperatorInboxMessages, "unacknowledged messages")
	}
	if bytes+size.bytes > OperatorInboxBytes {
		return inboxFull("operator inbox bytes", bytes+size.bytes, OperatorInboxBytes, "unacknowledged bytes")
	}
	return nil
}

// ValidateOperatorReply accepts no correlation, or a reply to an operator
// message whose frozen recipients include the sender's logical ID.
func (s *Store) ValidateOperatorReply(ctx context.Context, sender, inReplyTo string) error {
	if inReplyTo == "" {
		return nil
	}
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_metadata mm JOIN message_receipts r USING(message_id) WHERE mm.message_id = ? AND mm.origin = 'operator' AND r.agent_id = ?`, inReplyTo, sender).Scan(&found)
	if err != nil {
		return err
	}
	if found == 0 {
		return ErrInvalidReply
	}
	return nil
}

// publicParent matches a public message: an operator "*", or an agent "*"
// whose frozen recipients kept the operator inbox (a legacy agent-only
// broadcast never qualifies). The sender must have been a recipient, or be
// the author of an agent's public message.
const publicParent = `SELECT COUNT(*) FROM message_metadata mm JOIN messages m USING(seq) WHERE mm.message_id = ? AND m.recipient = '*' AND (
	(mm.origin = 'operator' AND EXISTS (SELECT 1 FROM message_receipts r WHERE r.message_id = mm.message_id AND r.agent_id = ?))
	OR (mm.origin = 'agent' AND EXISTS (SELECT 1 FROM durable_inbox o WHERE o.message_id = mm.message_id AND o.agent_id = 'operator')
		AND (m.sender = ? OR EXISTS (SELECT 1 FROM message_receipts r WHERE r.message_id = mm.message_id AND r.agent_id = ?))))`

// ValidatePublicReply accepts no correlation, or a reply to a public message
// the sender took part in. Private parents are never revealed or promoted.
func (s *Store) ValidatePublicReply(ctx context.Context, sender, inReplyTo string) error {
	if inReplyTo == "" {
		return nil
	}
	var found int
	if err := s.db.QueryRowContext(ctx, publicParent, inReplyTo, sender, sender, sender).Scan(&found); err != nil {
		return err
	}
	if found == 0 {
		return errPublicReply
	}
	return nil
}

// publicReplyError keeps the invalid_reply mapping (errors.Is ErrInvalidReply)
// with a reason that names the public-parent rule first.
type publicReplyError struct{}

func (*publicReplyError) Error() string {
	return "a public reply's in_reply_to must name a public message this sender received or wrote"
}

func (*publicReplyError) Is(target error) bool { return target == ErrInvalidReply }

// errPublicReply is the public-thread variant of ErrInvalidReply.
var errPublicReply = &publicReplyError{}

// ReadOperatorPage returns a bounded page of unacknowledged operator-inbox
// messages. It changes no receipts.
func (s *Store) ReadOperatorPage(ctx context.Context, req protocol.PageRequest) (protocol.OperatorPage, error) {
	limit, err := pageLimit(req.Limit)
	if err != nil {
		return protocol.OperatorPage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return protocol.OperatorPage{}, err
	}
	defer tx.Rollback()
	page, err := scanBounds(ctx, tx, req)
	if err != nil {
		return protocol.OperatorPage{}, err
	}
	if page.Messages, err = pageMessages(ctx, tx, page, limit); err != nil {
		return protocol.OperatorPage{}, err
	}
	if n := len(page.Messages); n > 0 {
		page.NextAfterSeq = page.Messages[n-1].Seq
	}
	page.More, err = pendingInRange(ctx, tx, page.NextAfterSeq, page.UpperSeq)
	return page, err
}

func pageLimit(limit int) (int, error) {
	if limit == 0 {
		return protocol.OperatorPageDefault, nil
	}
	if limit < 0 || limit > protocol.OperatorPageMax {
		return 0, ErrInvalidLimit
	}
	return limit, nil
}

// scanBounds fixes the scan's upper bound from committed history, never from
// in-memory sequence allocation, which also covers unpersisted welcomes.
func scanBounds(ctx context.Context, tx *sql.Tx, req protocol.PageRequest) (protocol.OperatorPage, error) {
	var committed uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM messages`).Scan(&committed); err != nil {
		return protocol.OperatorPage{}, err
	}
	upper := req.UpperSeq
	if upper == 0 {
		upper = committed
	}
	if upper > committed || req.AfterSeq > upper {
		return protocol.OperatorPage{}, ErrInvalidCursor
	}
	return protocol.OperatorPage{Messages: []protocol.Message{}, NextAfterSeq: req.AfterSeq, UpperSeq: upper}, nil
}

func pageMessages(ctx context.Context, tx *sql.Tx, page protocol.OperatorPage, limit int) ([]protocol.Message, error) {
	query := `SELECT ` + durableColumns + ` FROM durable_inbox i JOIN message_metadata mm USING(message_id) JOIN messages m USING(seq) WHERE i.agent_id = ? AND i.acknowledged_at IS NULL AND m.seq > ? AND m.seq <= ? ORDER BY m.seq LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, protocol.OperatorID, page.NextAfterSeq, page.UpperSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []protocol.Message{}
	budget := protocol.MaxFrameBytes - pageEnvelopeReserve
	for rows.Next() {
		msg, err := scanDurableMessage(rows)
		if err != nil {
			return nil, err
		}
		msg.Payload = retainedPayload(string(msg.Payload))
		size, err := json.Marshal(msg)
		if err != nil {
			return nil, err
		}
		// Payloads are never truncated; a message that doesn't fit waits for the next page.
		if len(messages) > 0 && len(size)+1 > budget {
			break
		}
		budget -= len(size) + 1
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

func pendingInRange(ctx context.Context, tx *sql.Tx, after, upper uint64) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM durable_inbox i JOIN message_metadata mm USING(message_id) WHERE i.agent_id = ? AND i.acknowledged_at IS NULL AND mm.seq > ? AND mm.seq <= ?`, protocol.OperatorID, after, upper).Scan(&count)
	return count > 0, err
}

// OperatorTransition is a first stage change to notify the original sender session.
type OperatorTransition struct {
	MessageID string
	Sender    protocol.Recipient
	Stage     string
}

// MarkOperatorReceived records that the operator client adopted these
// messages. All IDs are validated before any write; stages never regress.
func (s *Store) MarkOperatorReceived(ctx context.Context, ids []string, audit OperatorAudit) ([]protocol.ReceiptState, []OperatorTransition, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	rows, err := operatorRows(ctx, tx, ids)
	if err != nil {
		return nil, nil, err
	}
	var receipts []protocol.ReceiptState
	var transitions []OperatorTransition
	now := time.Now().UTC()
	for _, row := range rows {
		if !row.received.Valid {
			if err := updateDurableReceipt(ctx, tx, operatorStage(row.id, protocol.StageAdapterReceived), now); err != nil {
				return nil, nil, err
			}
			row.received = sql.NullTime{Time: now, Valid: true}
			transitions = append(transitions, OperatorTransition{row.id, row.sender, protocol.StageAdapterReceived})
		}
		receipts = append(receipts, row.receipt())
	}
	if err := insertAudit(ctx, tx, audit); err != nil {
		return nil, nil, err
	}
	return receipts, transitions, tx.Commit()
}

// AcknowledgeOperator records the human's explicit receipt of one message.
// It requires the received stage and is idempotent.
func (s *Store) AcknowledgeOperator(ctx context.Context, id string, audit OperatorAudit) (*OperatorTransition, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := operatorRows(ctx, tx, []string{id})
	if err != nil {
		return nil, err
	}
	row := rows[0]
	if !row.received.Valid {
		return nil, ErrInvalidAck
	}
	var transition *OperatorTransition
	if !row.acknowledged.Valid {
		if err := updateDurableReceipt(ctx, tx, operatorStage(id, protocol.StageAgentAcknowledged), time.Now().UTC()); err != nil {
			return nil, err
		}
		transition = &OperatorTransition{id, row.sender, protocol.StageAgentAcknowledged}
	}
	if err := insertAudit(ctx, tx, audit); err != nil {
		return nil, err
	}
	return transition, tx.Commit()
}

type inboxRow struct {
	id                     string
	sender                 protocol.Recipient
	received, acknowledged sql.NullTime
}

func (r inboxRow) receipt() protocol.ReceiptState {
	stage := protocol.StageAccepted
	if r.received.Valid {
		stage = protocol.StageAdapterReceived
	}
	if r.acknowledged.Valid {
		stage = protocol.StageAgentAcknowledged
	}
	return protocol.ReceiptState{MessageID: r.id, AgentID: protocol.OperatorID, Stage: stage}
}

// operatorRows loads every requested operator-inbox row, or fails with
// ErrInvalidAck before anything is written.
func operatorRows(ctx context.Context, tx *sql.Tx, ids []string) ([]inboxRow, error) {
	rows := make([]inboxRow, 0, len(ids))
	for _, id := range ids {
		row := inboxRow{id: id}
		err := tx.QueryRowContext(ctx, `SELECT m.sender, mm.sender_session, i.received_at, i.acknowledged_at FROM durable_inbox i JOIN message_metadata mm USING(message_id) JOIN messages m USING(seq) WHERE i.message_id = ? AND i.agent_id = ?`, id, protocol.OperatorID).
			Scan(&row.sender.AgentID, &row.sender.SessionID, &row.received, &row.acknowledged)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidAck
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func operatorStage(id, stage string) Acknowledgment {
	return Acknowledgment{MessageID: id, Recipient: protocol.Recipient{AgentID: protocol.OperatorID}, Stage: stage}
}

func insertAudit(ctx context.Context, db execer, a OperatorAudit) error {
	_, err := db.ExecContext(ctx, `INSERT INTO operator_audit (ts, action, principal_uid, message_id, scope, result, detail) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		time.Now().UTC(), a.Action, a.PrincipalUID, nullString(a.MessageID), nullString(a.Scope), a.Result, nullString(a.Detail))
	return err
}
