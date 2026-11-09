package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Classifier predictions (#339, shadow mode): one row per operator message
// the classifier looked at, recorded and never applied. The first row
// wins; a replay, a restart or a duplicate job changes nothing.

const predictionsSchema = `
CREATE TABLE IF NOT EXISTS classifier_predictions (
    operator_message_id TEXT PRIMARY KEY,
    outcome             TEXT NOT NULL,
    reason              TEXT NOT NULL DEFAULT '',
    agent_id            TEXT NOT NULL DEFAULT '',
    confidence          REAL NOT NULL DEFAULT 0,
    probabilities       TEXT NOT NULL DEFAULT '{}',
    abstain_probability REAL NOT NULL DEFAULT 0,
    eligible            TEXT NOT NULL DEFAULT '[]',
    provider            TEXT NOT NULL DEFAULT '',
    model               TEXT NOT NULL DEFAULT '',
    model_version       TEXT NOT NULL DEFAULT '',
    threshold           REAL NOT NULL DEFAULT 0,
    latency_ms          INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL
);
`

// Prediction outcomes.
const (
	PredictionPredicted = "predicted" // agent_id names the agent, confidence at or above the threshold
	PredictionAbstain   = "abstain"   // the model chose none, or below the threshold
	PredictionError     = "error"     // the provider failed; reason has the class
	PredictionTimeout   = "timeout"   // the provider did not answer in time
	PredictionSkipped   = "skipped"   // not asked; reason says why
)

// Prediction is one recorded decision.
type Prediction struct {
	MessageID          string
	Outcome            string
	Reason             string
	AgentID            string
	Confidence         float64
	Probabilities      map[string]float64 // by agent ID
	AbstainProbability float64            // the abstain choice's
	Eligible           []string
	Provider           string
	Model              string
	ModelVersion       string
	Threshold          float64
	LatencyMS          int64
	CreatedAt          time.Time
}

const predictionColumns = `operator_message_id, outcome, reason, agent_id, confidence, probabilities, abstain_probability, eligible, provider, model, model_version, threshold, latency_ms, created_at`

// RecordPrediction stores the decision unless one is already there for the
// message; inserted says which.
func (s *Store) RecordPrediction(ctx context.Context, p Prediction) (inserted bool, err error) {
	if p.MessageID == "" || p.Outcome == "" {
		return false, errors.New("prediction needs a message ID and an outcome")
	}
	probabilities, err := json.Marshal(nonNilMap(p.Probabilities))
	if err != nil {
		return false, err
	}
	eligible, err := json.Marshal(nonNilList(p.Eligible))
	if err != nil {
		return false, err
	}
	at := p.CreatedAt
	if at.IsZero() {
		at = time.Now()
	}
	result, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO classifier_predictions (`+predictionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.MessageID, p.Outcome, p.Reason, p.AgentID, p.Confidence, string(probabilities), p.AbstainProbability, string(eligible),
		p.Provider, p.Model, p.ModelVersion, p.Threshold, p.LatencyMS, stamp(at))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// Prediction is the decision recorded for the message, or nil.
func (s *Store) Prediction(ctx context.Context, messageID string) (*Prediction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+predictionColumns+` FROM classifier_predictions WHERE operator_message_id = ?`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found, err := scanPredictions(rows)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &found[0], nil
}

// PredictionsSince lists the newest decisions recorded at or after since,
// at most limit of them, oldest first; more says whether older ones were
// left out.
func (s *Store) PredictionsSince(ctx context.Context, since time.Time, limit int) (found []Prediction, more bool, err error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+predictionColumns+` FROM classifier_predictions WHERE created_at >= ? ORDER BY created_at DESC, operator_message_id DESC LIMIT ?`,
		stamp(since), limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	newest, err := scanPredictions(rows)
	if err != nil {
		return nil, false, err
	}
	if len(newest) > limit {
		newest, more = newest[:limit], true
	}
	for i, j := 0, len(newest)-1; i < j; i, j = i+1, j-1 {
		newest[i], newest[j] = newest[j], newest[i]
	}
	return newest, more, nil
}

// ReplyOwner is the agent holding the message's reply claim as owner,
// active or done, or ""; a released claim holds nothing. Read directly,
// for the operator's report; no recipient check applies.
func (s *Store) ReplyOwner(ctx context.Context, messageID string) (string, error) {
	var agent string
	err := s.db.QueryRowContext(ctx, `SELECT agent_id FROM claims WHERE operator_message_id = ? AND task = ? AND role = ? AND state != ? ORDER BY claimed_at LIMIT 1`,
		messageID, protocol.TaskReply, protocol.RoleOwner, protocol.ClaimReleased).Scan(&agent)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return agent, err
}

func scanPredictions(rows *sql.Rows) ([]Prediction, error) {
	var out []Prediction
	for rows.Next() {
		var p Prediction
		var probabilities, eligible, at string
		if err := rows.Scan(&p.MessageID, &p.Outcome, &p.Reason, &p.AgentID, &p.Confidence, &probabilities, &p.AbstainProbability, &eligible,
			&p.Provider, &p.Model, &p.ModelVersion, &p.Threshold, &p.LatencyMS, &at); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(probabilities), &p.Probabilities); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(eligible), &p.Eligible); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, p)
	}
	return out, rows.Err()
}

func nonNilMap(m map[string]float64) map[string]float64 {
	if m == nil {
		return map[string]float64{}
	}
	return m
}

func nonNilList(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}
