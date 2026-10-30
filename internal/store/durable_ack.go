package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func (s *Store) isDurable(ctx context.Context, id string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM durable_messages WHERE message_id = ?`, id).Scan(&count)
	return count != 0, err
}

type durableReceipt struct {
	sender       protocol.Recipient
	session      string
	received     sql.NullTime
	acknowledged sql.NullTime
	origin       sql.NullString
}

// AckReport says whom to notify and whether this acknowledgment was a first
// transition on an operator-sent message, which the live session reports.
type AckReport struct {
	Sender        protocol.Recipient
	FirstOperator bool
	// RemoteContact is the paired workspace a first agent acknowledgment of
	// its message is queued for, with the acknowledgment (#222).
	RemoteContact string
}

func (s *Store) acknowledgeDurable(ctx context.Context, ack Acknowledgment) (AckReport, error) {
	if ack.ProtocolVersion < protocol.DurableVersion {
		return AckReport{}, ErrInvalidAck
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AckReport{}, err
	}
	defer tx.Rollback()
	report, err := applyDurableAck(ctx, tx, ack)
	if err != nil {
		return AckReport{}, err
	}
	return report, tx.Commit()
}

// applyDurableAck validates and records one acknowledgment inside tx.
func applyDurableAck(ctx context.Context, tx *sql.Tx, ack Acknowledgment) (AckReport, error) {
	receipt, err := readDurableReceipt(ctx, tx, ack)
	if err != nil {
		return AckReport{}, err
	}
	if err := receipt.validate(ack); err != nil {
		return AckReport{}, err
	}
	report := AckReport{Sender: receipt.sender, FirstOperator: receipt.firstOperatorTransition(ack.Stage)}
	if receipt.acknowledged.Valid {
		return report, nil
	}
	now := time.Now().UTC()
	if err := updateDurableReceipt(ctx, tx, ack, now); err != nil {
		return AckReport{}, err
	}
	if ack.Stage == protocol.StageAgentAcknowledged && receipt.origin.String == protocol.OriginRemote {
		report.RemoteContact, err = queueAgentReadIn(ctx, tx, agentRead{ack.MessageID, ack.Recipient.AgentID, now})
	}
	return report, err
}

func (r durableReceipt) firstOperatorTransition(stage string) bool {
	return r.firstTransition(stage) && r.origin.String == protocol.OriginOperator
}

func (r durableReceipt) firstTransition(stage string) bool {
	if r.acknowledged.Valid {
		return false
	}
	return stage == protocol.StageAgentAcknowledged || !r.received.Valid
}

func readDurableReceipt(ctx context.Context, tx *sql.Tx, ack Acknowledgment) (durableReceipt, error) {
	var r durableReceipt
	query := `SELECT m.sender, mm.sender_session, i.session_id, i.received_at, i.acknowledged_at, mm.origin FROM durable_inbox i JOIN message_metadata mm USING(message_id) JOIN messages m USING(seq) WHERE i.message_id = ? AND i.agent_id = ?`
	err := tx.QueryRowContext(ctx, query, ack.MessageID, ack.Recipient.AgentID).Scan(&r.sender.AgentID, &r.sender.SessionID, &r.session, &r.received, &r.acknowledged, &r.origin)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrInvalidAck
	}
	return r, err
}

func (r durableReceipt) validate(ack Acknowledgment) error {
	if ack.Stage != protocol.StageAdapterReceived && ack.Stage != protocol.StageAgentAcknowledged {
		return ErrInvalidAck
	}
	// A committed acknowledgment can be retried by the current logical owner,
	// even when its confirmation was lost and the original session is gone.
	if r.acknowledged.Valid {
		return nil
	}
	if r.session != ack.Recipient.SessionID {
		return ErrInvalidAck
	}
	if ack.Stage == protocol.StageAgentAcknowledged && !r.received.Valid {
		return ErrInvalidAck
	}
	return nil
}

func updateDurableReceipt(ctx context.Context, tx *sql.Tx, ack Acknowledgment, now time.Time) error {
	column := "received_at"
	legacyColumn := "adapter_received_at"
	if ack.Stage == protocol.StageAgentAcknowledged {
		column, legacyColumn = "acknowledged_at", "agent_acknowledged_at"
	}
	query := `UPDATE durable_inbox SET ` + column + ` = COALESCE(` + column + `, ?) WHERE message_id = ? AND agent_id = ?`
	if _, err := tx.ExecContext(ctx, query, now, ack.MessageID, ack.Recipient.AgentID); err != nil {
		return err
	}
	query = `UPDATE message_receipts SET ` + legacyColumn + ` = COALESCE(` + legacyColumn + `, ?) WHERE message_id = ? AND agent_id = ?`
	_, err := tx.ExecContext(ctx, query, now, ack.MessageID, ack.Recipient.AgentID)
	return err
}
