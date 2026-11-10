package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Decision requests (#352): one row per request, keyed by its anchor (the
// agent's durable chat message to the operator). The first write wins: a
// replay by anchor or by (agent, key) returns the row that exists.

const decisionsSchema = `
CREATE TABLE IF NOT EXISTS decision_requests (
    id                TEXT PRIMARY KEY,
    agent_id          TEXT NOT NULL,
    request_key       TEXT NOT NULL DEFAULT '',
    anchor_message_id TEXT NOT NULL UNIQUE,
    summary           TEXT NOT NULL,
    state             TEXT NOT NULL,
    delivery          TEXT NOT NULL,
    reason            TEXT NOT NULL DEFAULT '',
    attempts          INTEGER NOT NULL DEFAULT 0,
    created_at        TEXT NOT NULL,
    answered_at       TEXT,
    posted_at         TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS decision_requests_key ON decision_requests(agent_id, request_key) WHERE request_key != '';
CREATE INDEX IF NOT EXISTS decision_requests_pending ON decision_requests(delivery) WHERE delivery = 'pending';
`

const decisionColumns = `id, agent_id, request_key, anchor_message_id, summary, state, delivery, reason, attempts, created_at, answered_at, posted_at`

// Decision is one row.
type Decision struct {
	ID, Agent, Key, Anchor, Summary, State, Delivery, Reason string
	Attempts                                                 int
	CreatedAt                                                time.Time
	AnsweredAt, PostedAt                                     *time.Time
}

// View is the decision as the protocol carries it.
func (d Decision) View(replayed bool) protocol.Decision {
	return protocol.Decision{ID: d.ID, Agent: d.Agent, Key: d.Key, Anchor: d.Anchor, Summary: d.Summary, State: d.State,
		Delivery: d.Delivery, Reason: d.Reason, Replayed: replayed, CreatedAt: d.CreatedAt, AnsweredAt: d.AnsweredAt}
}

// AnchorOwnedBy: the message is durable, sent by the agent, to the operator.
func (s *Store) AnchorOwnedBy(ctx context.Context, messageID, agent string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM message_metadata md
		JOIN messages m ON m.seq = md.seq JOIN durable_messages d ON d.message_id = md.message_id
		WHERE md.message_id = ? AND m.sender = ? AND m.recipient = ?`, messageID, agent, protocol.OperatorID).Scan(&n)
	return n > 0, err
}

// CreateDecision records the request unless one exists for its anchor or
// its (agent, key); existing says which happened, and the row returned is
// the one that stands.
func (s *Store) CreateDecision(ctx context.Context, d Decision) (Decision, bool, error) {
	if d.ID == "" || d.Agent == "" || d.Anchor == "" || d.Summary == "" {
		return Decision{}, false, errors.New("a decision needs an id, an agent, an anchor and a summary")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Decision{}, false, err
	}
	defer tx.Rollback()
	if existing, err := decisionBy(ctx, tx, d.Agent, d.Key, d.Anchor); err != nil {
		return Decision{}, false, err
	} else if existing != nil {
		return *existing, true, nil
	}
	at := d.CreatedAt
	if at.IsZero() {
		at = time.Now()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO decision_requests (`+decisionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, NULL, NULL)`,
		d.ID, d.Agent, d.Key, d.Anchor, d.Summary, protocol.DecisionOpen, d.Delivery, d.Reason, stamp(at)); err != nil {
		return Decision{}, false, err
	}
	created, err := decisionBy(ctx, tx, d.Agent, d.Key, d.Anchor)
	if err != nil {
		return Decision{}, false, err
	}
	return *created, false, tx.Commit()
}

// decisionBy finds the row for the anchor, or for the agent's key.
func decisionBy(ctx context.Context, tx *sql.Tx, agent, key, anchor string) (*Decision, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+decisionColumns+` FROM decision_requests
		WHERE anchor_message_id = ? OR (request_key != '' AND agent_id = ? AND request_key = ?) LIMIT 1`, anchor, agent, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found, err := scanDecisions(rows)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &found[0], nil
}

// Decision is the row by id, or nil.
func (s *Store) Decision(ctx context.Context, id string) (*Decision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+decisionColumns+` FROM decision_requests WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found, err := scanDecisions(rows)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &found[0], nil
}

// MarkDecisionDelivery records one attempt's outcome and its class.
func (s *Store) MarkDecisionDelivery(ctx context.Context, id, delivery, reason string, at time.Time) error {
	var posted any
	if delivery == protocol.DeliverySent {
		posted = stamp(at)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE decision_requests SET delivery = ?, reason = ?, attempts = attempts + 1,
		posted_at = COALESCE(?, posted_at) WHERE id = ?`, delivery, reason, posted, id)
	return err
}

// MarkDecisionAnswered closes the open request behind the anchor; it says
// whether one was open.
func (s *Store) MarkDecisionAnswered(ctx context.Context, anchor string, at time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE decision_requests SET state = ?, answered_at = ? WHERE anchor_message_id = ? AND state = ?`,
		protocol.DecisionAnswered, stamp(at), anchor, protocol.DecisionOpen)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// PendingDecisions are the open requests whose notice has not been sent,
// oldest first: what a restarted worker takes up again.
func (s *Store) PendingDecisions(ctx context.Context, limit int) ([]Decision, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+decisionColumns+` FROM decision_requests
		WHERE delivery = ? AND state = ? ORDER BY created_at, id LIMIT ?`, protocol.DeliveryPending, protocol.DecisionOpen, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDecisions(rows)
}

// DecisionsSince lists the newest requests created at or after since, at
// most limit, oldest first.
func (s *Store) DecisionsSince(ctx context.Context, since time.Time, limit int) ([]Decision, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+decisionColumns+` FROM decision_requests WHERE created_at >= ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, stamp(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	newest, err := scanDecisions(rows)
	for i, j := 0, len(newest)-1; i < j; i, j = i+1, j-1 {
		newest[i], newest[j] = newest[j], newest[i]
	}
	return newest, err
}

func scanDecisions(rows *sql.Rows) ([]Decision, error) {
	var out []Decision
	for rows.Next() {
		var d Decision
		var created string
		var answered, posted sql.NullString
		if err := rows.Scan(&d.ID, &d.Agent, &d.Key, &d.Anchor, &d.Summary, &d.State, &d.Delivery, &d.Reason, &d.Attempts,
			&created, &answered, &posted); err != nil {
			return nil, err
		}
		d.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		d.AnsweredAt, d.PostedAt = stampTime(answered), stampTime(posted)
		out = append(out, d)
	}
	return out, rows.Err()
}

func stampTime(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &t
}
