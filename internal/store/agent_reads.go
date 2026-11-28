package store

import (
	"context"
	"database/sql"
	"time"
)

// AgentRead is one durable agent's reading of its inbox: the messages it
// hasn't acknowledged, the oldest one's time, and its last acknowledgment.
type AgentRead struct {
	AgentID        string
	Unread         int
	OldestUnreadAt *time.Time
	LastReadAt     *time.Time
}

// AgentReads is every durable agent's reading, by agent ID. It only reads.
func (s *Store) AgentReads(ctx context.Context) ([]AgentRead, error) {
	ids, err := s.durableAgentIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AgentRead, 0, len(ids))
	for _, id := range ids {
		r, err := s.agentRead(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Store) durableAgentIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM durable_agents ORDER BY agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// agentRead is one agent's reading. The times are read as their typed
// columns, never through an aggregate, so they scan as times.
func (s *Store) agentRead(ctx context.Context, id string) (AgentRead, error) {
	r := AgentRead{AgentID: id}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM durable_inbox WHERE agent_id = ? AND acknowledged_at IS NULL`, id).Scan(&r.Unread)
	if err != nil {
		return r, err
	}
	if r.OldestUnreadAt, err = s.oneTime(ctx, `SELECT m.ts FROM durable_inbox i
JOIN message_metadata md ON md.message_id = i.message_id JOIN messages m ON m.seq = md.seq
WHERE i.agent_id = ? AND i.acknowledged_at IS NULL ORDER BY m.seq LIMIT 1`, id); err != nil {
		return r, err
	}
	r.LastReadAt, err = s.oneTime(ctx, `SELECT acknowledged_at FROM durable_inbox
WHERE agent_id = ? AND acknowledged_at IS NOT NULL ORDER BY acknowledged_at DESC LIMIT 1`, id)
	return r, err
}

// oneTime is the single time query selects for id, or nil when it selects none.
func (s *Store) oneTime(ctx context.Context, query, id string) (*time.Time, error) {
	var t time.Time
	err := s.db.QueryRowContext(ctx, query, id).Scan(&t)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t = t.UTC()
	return &t, nil
}
