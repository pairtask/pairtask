package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Agent roles (#339, calibration): each agent's own one-line role, kept
// across sessions and restarts, shown to the shadow classifier.

const agentRolesSchema = `
CREATE TABLE IF NOT EXISTS agent_roles (
    agent_id   TEXT PRIMARY KEY,
    role       TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
`

// SetAgentRole stores the agent's role; "" clears it. The role as it now
// stands is returned.
func (s *Store) SetAgentRole(ctx context.Context, agent, role string, at time.Time) (protocol.AgentRole, error) {
	if agent == "" {
		return protocol.AgentRole{}, errors.New("a role needs an agent")
	}
	if role == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM agent_roles WHERE agent_id = ?`, agent)
		return protocol.AgentRole{AgentID: agent, UpdatedAt: at.UTC()}, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_roles (agent_id, role, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET role = excluded.role, updated_at = excluded.updated_at`, agent, role, stamp(at))
	return protocol.AgentRole{AgentID: agent, Role: role, UpdatedAt: at.UTC()}, err
}

// AgentRole is the agent's declared role, "" when none.
func (s *Store) AgentRole(ctx context.Context, agent string) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT role FROM agent_roles WHERE agent_id = ?`, agent).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// AgentRoles are all declared roles, by agent ID.
func (s *Store) AgentRoles(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id, role FROM agent_roles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roles := map[string]string{}
	for rows.Next() {
		var agent, role string
		if err := rows.Scan(&agent, &role); err != nil {
			return nil, err
		}
		roles[agent] = role
	}
	return roles, rows.Err()
}

// PredictionsFor are the newest answers the model gave (a pick or an
// abstention) where the agent was eligible, at most limit, newest first.
// Skipped, failed and timed-out rows say nothing about the agent's role and
// are left out, so they can't crowd the window.
func (s *Store) PredictionsFor(ctx context.Context, agent string, limit int) ([]Prediction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+predictionColumns+` FROM classifier_predictions
		WHERE outcome IN (?, ?) AND EXISTS (SELECT 1 FROM json_each(eligible) WHERE value = ?)
		ORDER BY created_at DESC, operator_message_id DESC LIMIT ?`, PredictionPredicted, PredictionAbstain, agent, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPredictions(rows)
}
