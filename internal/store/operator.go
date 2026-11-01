package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorSchema = `
CREATE TABLE IF NOT EXISTS operator_requests (
 message_id TEXT PRIMARY KEY,
 scope_kind TEXT NOT NULL,
 members TEXT,
 recipients TEXT NOT NULL,
 excluded TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS operator_audit (
 id INTEGER PRIMARY KEY,
 ts TIMESTAMP NOT NULL,
 action TEXT NOT NULL,
 principal_uid INTEGER,
 message_id TEXT,
 scope TEXT,
 result TEXT NOT NULL,
 detail TEXT
);
`

// Operator request scopes.
const (
	ScopeDirect = "direct"
	ScopeGroup  = "group"
	ScopeAll    = "all"
)

// Audit results. An intent row is written before routing and finished afterwards.
const (
	AuditPending   = "pending"
	AuditAccepted  = "accepted"
	AuditRepeated  = "repeated"
	AuditRejected  = "rejected"
	AuditForbidden = "forbidden"
)

// OperatorScope is the canonical request scope: sorted, deduplicated members.
// Members are nil for ScopeAll.
type OperatorScope struct {
	Kind    string
	Members []string
}

// Equal compares scopes for idempotent retries.
func (s OperatorScope) Equal(other OperatorScope) bool {
	return s.Kind == other.Kind && slices.Equal(s.Members, other.Members)
}

// OperatorAudit is one audit row. It never holds message payloads.
type OperatorAudit struct {
	Action       string
	PrincipalUID int
	MessageID    string
	Scope        string
	Result       string
	Detail       string
}

// RecordOperatorAudit commits an audit row on its own and returns its ID.
func (s *Store) RecordOperatorAudit(ctx context.Context, a OperatorAudit) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO operator_audit (ts, action, principal_uid, message_id, scope, result, detail) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		time.Now().UTC(), a.Action, a.PrincipalUID, nullString(a.MessageID), nullString(a.Scope), a.Result, nullString(a.Detail))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AuditOutcome is the result of an operator request; Detail is a fixed, payload-free reason.
type AuditOutcome struct {
	Result string
	Detail string
}

// FinishOperatorAudit records the outcome of an earlier intent row.
func (s *Store) FinishOperatorAudit(ctx context.Context, id int64, outcome AuditOutcome) error {
	return finishAudit(ctx, s.db, id, outcome)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func finishAudit(ctx context.Context, db execer, id int64, outcome AuditOutcome) error {
	res, err := db.ExecContext(ctx, `UPDATE operator_audit SET result = ?, detail = ? WHERE id = ? AND result = ?`, outcome.Result, nullString(outcome.Detail), id, AuditPending)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, errors.New("operator audit intent missing or already finished"))
	}
	return nil
}

// OperatorMessage is a routed operator message with its frozen scope and the
// audit intent row its result is committed to.
type OperatorMessage struct {
	Msg        protocol.Message
	Scope      OperatorScope
	Recipients []protocol.Recipient
	Excluded   []string
	AuditID    int64
	// Assignees are the tagged agents its reply is assigned to (#278),
	// stored with it or not at all.
	Assignees []string
}

// PersistOperatorMessage commits the message, its frozen scope and the audit
// result atomically, so a routed operator message is never left unaudited.
func (s *Store) PersistOperatorMessage(ctx context.Context, m OperatorMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := persistMessage(ctx, tx, routedMessage{msg: m.Msg, recipients: m.Recipients}); err != nil {
		return err
	}
	if err := insertOperatorRequest(ctx, tx, m); err != nil {
		return err
	}
	if err := insertAssignments(ctx, tx, m.Msg.MessageID, m.Assignees, operatorTime(m.Msg)); err != nil {
		return err
	}
	if err := finishAudit(ctx, tx, m.AuditID, AuditOutcome{Result: AuditAccepted}); err != nil {
		return err
	}
	return tx.Commit()
}

// operatorTime is when the message was sent, the time its assignments
// start.
func operatorTime(msg protocol.Message) time.Time {
	if msg.TS != nil {
		return *msg.TS
	}
	return time.Now().UTC()
}

func insertOperatorRequest(ctx context.Context, tx *sql.Tx, m OperatorMessage) error {
	members, err := jsonOrNull(m.Scope.Members)
	if err != nil {
		return err
	}
	frozen, err := json.Marshal(m.Recipients)
	if err != nil {
		return err
	}
	left, err := json.Marshal(nonNil(m.Excluded))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO operator_requests (message_id, scope_kind, members, recipients, excluded) VALUES (?, ?, ?, ?, ?)`, m.Msg.MessageID, m.Scope.Kind, members, string(frozen), string(left))
	return err
}

// OperatorRetry is the retained outcome of an earlier identical operator send.
type OperatorRetry struct {
	Seq        uint64
	Recipients []protocol.Recipient
	Excluded   []string
	Receipts   []protocol.ReceiptState
}

type storedMessage struct {
	seq       uint64
	origin    sql.NullString
	inReplyTo string
	payload   string
	room      sql.NullString
	mentions  sql.NullString
}

// matches holds only for a trusted operator message with the same
// correlation, room and content; a retry never moves to another room.
func (m storedMessage) matches(request protocol.Message) bool {
	return m.origin.String == protocol.OriginOperator && m.inReplyTo == request.InReplyTo && m.room.String == request.Room &&
		m.mentions == nullList(request.Mentions) && bytes.Equal(compactPayload(json.RawMessage(m.payload)), compactPayload(request.Payload))
}

// LookupOperatorRetry returns nil when the ID is unused. A used ID matches only
// a trusted operator message with the same scope, payload and correlation;
// anything else, including legacy rows claiming the operator sender, is a duplicate.
func (s *Store) LookupOperatorRetry(ctx context.Context, request protocol.Message, scope OperatorScope) (*OperatorRetry, error) {
	var stored storedMessage
	err := s.db.QueryRowContext(ctx, `SELECT m.seq, mm.origin, mm.in_reply_to, m.payload, mm.room, mm.mentions FROM message_metadata mm JOIN messages m USING(seq) WHERE mm.message_id = ?`, request.MessageID).Scan(&stored.seq, &stored.origin, &stored.inReplyTo, &stored.payload, &stored.room, &stored.mentions)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !stored.matches(request) {
		return nil, ErrDuplicateMessage
	}
	return s.operatorRetry(ctx, request.MessageID, stored.seq, scope)
}

func (s *Store) operatorRetry(ctx context.Context, id string, seq uint64, scope OperatorScope) (*OperatorRetry, error) {
	stored, retry, err := s.operatorRequest(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDuplicateMessage
	}
	if err != nil {
		return nil, err
	}
	if !stored.Equal(scope) {
		return nil, ErrDuplicateMessage
	}
	retry.Seq = seq
	snapshot := RetrySnapshot{Message: protocol.Message{MessageID: id}}
	if err := s.retryReceipts(ctx, &snapshot); err != nil {
		return nil, err
	}
	retry.Receipts = snapshot.Receipts
	return retry, nil
}

func (s *Store) operatorRequest(ctx context.Context, id string) (OperatorScope, *OperatorRetry, error) {
	var scope OperatorScope
	var members sql.NullString
	var recipients, excluded string
	err := s.db.QueryRowContext(ctx, `SELECT scope_kind, members, recipients, excluded FROM operator_requests WHERE message_id = ?`, id).Scan(&scope.Kind, &members, &recipients, &excluded)
	if err != nil {
		return scope, nil, err
	}
	if members.Valid {
		if err := json.Unmarshal([]byte(members.String), &scope.Members); err != nil {
			return scope, nil, err
		}
	}
	retry := &OperatorRetry{}
	if err := json.Unmarshal([]byte(recipients), &retry.Recipients); err != nil {
		return scope, nil, err
	}
	if err := json.Unmarshal([]byte(excluded), &retry.Excluded); err != nil {
		return scope, nil, err
	}
	return scope, retry, nil
}

func jsonOrNull(members []string) (sql.NullString, error) {
	if members == nil {
		return sql.NullString{}, nil
	}
	data, err := json.Marshal(members)
	return sql.NullString{String: string(data), Valid: true}, err
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// RecipientCapacity reports whether one more durable message of the given wire
// size fits the recipient's own pending limits.
func (s *Store) RecipientCapacity(ctx context.Context, agentID string, wireBytes int) (bool, error) {
	var count, bytes int
	query := `SELECT COUNT(*), COALESCE(SUM(d.wire_bytes),0) FROM durable_inbox i JOIN durable_messages d USING(message_id) WHERE i.agent_id = ? AND i.acknowledged_at IS NULL`
	if err := s.db.QueryRowContext(ctx, query, agentID).Scan(&count, &bytes); err != nil {
		return false, err
	}
	return count < MaxPendingMessages && bytes+wireBytes <= MaxPendingBytes, nil
}
