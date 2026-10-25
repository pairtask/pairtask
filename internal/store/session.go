package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

var ErrInvalidMessage = errors.New("message is not an operator-sent message")

// OperatorInboxState returns the newest pending operator-inbox sequence and
// the pending count.
func (s *Store) OperatorInboxState(ctx context.Context) (uint64, int, error) {
	var head uint64
	var pending int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(mm.seq), 0), COUNT(*) FROM durable_inbox i JOIN message_metadata mm USING(message_id) WHERE i.agent_id = ? AND i.acknowledged_at IS NULL`, protocol.OperatorID).Scan(&head, &pending)
	return head, pending, err
}

// SentQuery bounds a recent-sent read: messages, and stages per message.
type SentQuery struct {
	Limit      int
	StageLimit int
}

// StageQuery pages one message's recipient stages after an agent ID.
type StageQuery struct {
	MessageID  string
	AfterAgent string
	Limit      int
}

// RecentOperatorSent returns operator-sent messages, newest first, each with
// its current recipient stages.
func (s *Store) RecentOperatorSent(ctx context.Context, q SentQuery) ([]protocol.SentSummary, error) {
	sent, err := s.recentSent(ctx, q.Limit)
	if err != nil {
		return nil, err
	}
	for i := range sent {
		if err := s.attachStages(ctx, &sent[i], q.StageLimit); err != nil {
			return nil, err
		}
	}
	return sent, nil
}

func (s *Store) recentSent(ctx context.Context, limit int) ([]protocol.SentSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mm.message_id, m.seq, m.ts, m.recipient, COALESCE(mm.room, '') FROM message_metadata mm JOIN messages m USING(seq) WHERE mm.origin = 'operator' ORDER BY m.seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sent []protocol.SentSummary
	for rows.Next() {
		var m protocol.SentSummary
		if err := rows.Scan(&m.MessageID, &m.Seq, &m.TS, &m.To, &m.Room); err != nil {
			return nil, err
		}
		sent = append(sent, m)
	}
	return sent, rows.Err()
}

// attachStages reads one extra stage to mark truncated receipts.
func (s *Store) attachStages(ctx context.Context, m *protocol.SentSummary, stageLimit int) error {
	stages, err := s.recipientStages(ctx, StageQuery{MessageID: m.MessageID, Limit: stageLimit + 1})
	if err != nil {
		return err
	}
	m.ReceiptsTruncated = len(stages) > stageLimit
	m.Recipients = stages[:min(len(stages), stageLimit)]
	return nil
}

// MessageStages pages current recipient stages of one operator-sent message
// in agent-ID order. It changes nothing.
func (s *Store) MessageStages(ctx context.Context, q StageQuery) (protocol.StagePage, error) {
	page := protocol.StagePage{MessageID: q.MessageID, Recipients: []protocol.RecipientStage{}, NextAfterAgent: q.AfterAgent}
	if err := s.checkOperatorMessage(ctx, q.MessageID); err != nil {
		return page, err
	}
	if err := s.checkAgentCursor(ctx, q); err != nil {
		return page, err
	}
	extra := q
	extra.Limit++
	stages, err := s.recipientStages(ctx, extra)
	if err != nil {
		return page, err
	}
	page.More = len(stages) > q.Limit
	page.Recipients = stages[:min(len(stages), q.Limit)]
	if n := len(page.Recipients); n > 0 {
		page.NextAfterAgent = page.Recipients[n-1].AgentID
	}
	return page, nil
}

// checkOperatorMessage accepts only messages with the trusted operator origin.
func (s *Store) checkOperatorMessage(ctx context.Context, id string) error {
	var origin sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT origin FROM message_metadata WHERE message_id = ?`, id).Scan(&origin)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidMessage
	}
	if err != nil {
		return err
	}
	if origin.String != protocol.OriginOperator {
		return ErrInvalidMessage
	}
	return nil
}

func (s *Store) checkAgentCursor(ctx context.Context, q StageQuery) error {
	if q.AfterAgent == "" {
		return nil
	}
	var found int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_receipts WHERE message_id = ? AND agent_id = ?`, q.MessageID, q.AfterAgent).Scan(&found); err != nil {
		return err
	}
	if found == 0 {
		return ErrInvalidCursor
	}
	return nil
}

func (s *Store) recipientStages(ctx context.Context, q StageQuery) ([]protocol.RecipientStage, error) {
	return stagesIn(ctx, s.db, q)
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// stagesIn reads current recipient stages through db or an open transaction.
func stagesIn(ctx context.Context, db querier, q StageQuery) ([]protocol.RecipientStage, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.agent_id, COALESCE(i.session_id, r.session_id), i.received_at, i.acknowledged_at FROM message_receipts r LEFT JOIN durable_inbox i ON i.message_id = r.message_id AND i.agent_id = r.agent_id WHERE r.message_id = ? AND r.agent_id > ? ORDER BY r.agent_id LIMIT ?`, q.MessageID, q.AfterAgent, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stages := []protocol.RecipientStage{}
	for rows.Next() {
		var st protocol.RecipientStage
		var received, acknowledged sql.NullTime
		if err := rows.Scan(&st.AgentID, &st.SessionID, &received, &acknowledged); err != nil {
			return nil, err
		}
		st.Stage = currentStage(received, acknowledged)
		stages = append(stages, st)
	}
	return stages, rows.Err()
}

func currentStage(received, acknowledged sql.NullTime) string {
	switch {
	case acknowledged.Valid:
		return protocol.StageAgentAcknowledged
	case received.Valid:
		return protocol.StageAdapterReceived
	}
	return protocol.StageAccepted
}

// RecordSessionAudit writes a session open or close row.
func (s *Store) RecordSessionAudit(ctx context.Context, action string, uid int, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO operator_audit (ts, action, principal_uid, scope, result) VALUES (?, ?, ?, ?, ?)`, time.Now().UTC(), action, uid, sessionID, AuditAccepted)
	return err
}
