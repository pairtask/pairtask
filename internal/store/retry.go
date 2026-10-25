package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pairtask/pairtask/internal/protocol"
)

type RetrySnapshot struct {
	Message    protocol.Message
	Recipients []protocol.Recipient
	Receipts   []protocol.ReceiptState
}

// LookupRetry does not create a message, change membership, rebind a sender, or
// reroute. It reports retained state to the same logical sender after reconnect.
func (s *Store) LookupRetry(ctx context.Context, request protocol.Message) (*RetrySnapshot, error) {
	query := `SELECT ` + durableColumns + ` FROM durable_messages d JOIN message_metadata mm USING(message_id) JOIN messages m USING(seq) WHERE d.message_id = ?`
	msg, err := scanDurableMessage(s.db.QueryRowContext(ctx, query, request.MessageID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !sameRequest(msg, request) {
		return nil, ErrDuplicateMessage
	}
	out := &RetrySnapshot{Message: msg}
	if err := s.retryReceipts(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func sameRequest(a, b protocol.Message) bool {
	if !b.Durable || !b.AckRequested {
		return false
	}
	if a.From != b.From || a.To != b.To {
		return false
	}
	if a.Room != b.Room {
		return false // a retry never moves to another room
	}
	if a.InReplyTo != b.InReplyTo {
		return false
	}
	return bytes.Equal(compactPayload(a.Payload), compactPayload(b.Payload))
}

func compactPayload(data json.RawMessage) []byte {
	var b bytes.Buffer
	if json.Compact(&b, data) != nil {
		return data
	}
	return b.Bytes()
}

func (s *Store) retryReceipts(ctx context.Context, snapshot *RetrySnapshot) error {
	query := `SELECT r.agent_id,r.session_id,i.session_id,i.received_at,i.acknowledged_at FROM message_receipts r JOIN durable_inbox i ON i.message_id = r.message_id AND i.agent_id = r.agent_id WHERE r.message_id = ? ORDER BY r.agent_id`
	rows, err := s.db.QueryContext(ctx, query, snapshot.Message.MessageID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var member protocol.Recipient
		var receipt durableReceipt
		if err := rows.Scan(&member.AgentID, &member.SessionID, &receipt.session, &receipt.received, &receipt.acknowledged); err != nil {
			return err
		}
		snapshot.Recipients = append(snapshot.Recipients, member)
		snapshot.Receipts = append(snapshot.Receipts, receipt.event(member.AgentID))
	}
	return rows.Err()
}

func (r durableReceipt) event(agent string) protocol.ReceiptState {
	event := protocol.ReceiptState{AgentID: agent, SessionID: r.session, Stage: protocol.StageAccepted}
	if r.received.Valid {
		event.Stage = protocol.StageAdapterReceived
	}
	if r.acknowledged.Valid {
		event.Stage = protocol.StageAgentAcknowledged
	}
	return event
}
