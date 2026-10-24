package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// agentStatusSchema keeps each agent's latest declaration with the session
// that made it. Times are RFC 3339 text with nanoseconds, so a record read back
// is exactly the record confirmed.
const agentStatusSchema = `
CREATE TABLE IF NOT EXISTS agent_status (
    agent_id    TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL,
    state       TEXT NOT NULL,
    task        TEXT NOT NULL,
    step        TEXT NOT NULL,
    waiting_on  TEXT NOT NULL,
    ref         TEXT NOT NULL,
    declared_at TEXT NOT NULL
);
`

// SetAgentStatus replaces the agent's record.
func (s *Store) SetAgentStatus(ctx context.Context, st protocol.AgentStatus) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_status (agent_id, session_id, state, task, step, waiting_on, ref, declared_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(agent_id) DO UPDATE SET session_id = excluded.session_id, state = excluded.state, task = excluded.task,
    step = excluded.step, waiting_on = excluded.waiting_on, ref = excluded.ref, declared_at = excluded.declared_at`,
		st.AgentID, st.SessionID, st.State, st.Task, st.Step, st.WaitingOn, st.Ref, st.DeclaredAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store agent status: %w", err)
	}
	return nil
}

// AgentStatus is the agent's latest record, or nil when it never declared one.
func (s *Store) AgentStatus(ctx context.Context, agentID string) (*protocol.AgentStatus, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+statusColumns+` FROM agent_status WHERE agent_id = ?`, agentID)
	st, err := scanStatus(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// StatusPage is the operator's bounded selection of records: the records of
// the given agents first, then other agents by ID, at most limit, and the
// number of agents with a record.
type StatusPage struct {
	Records []protocol.AgentStatus
	Total   int
}

// Current is an agent and the session that owns it now.
type Current struct {
	AgentID, SessionID string
}

// AgentStatusPage selects first the records declared by the agents' current
// sessions, then fills up to limit with the others by agent ID; a record from
// an older session of a connected agent is one of the others.
func (s *Store) AgentStatusPage(ctx context.Context, current []Current, limit int) (StatusPage, error) {
	var page StatusPage
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_status`).Scan(&page.Total); err != nil {
		return page, fmt.Errorf("count agent statuses: %w", err)
	}
	records, err := s.currentRecords(ctx, current, limit)
	if err != nil {
		return page, err
	}
	rest, err := s.statusesByID(ctx, limit+len(records))
	if err != nil {
		return page, err
	}
	page.Records = fillRecords(records, rest, limit)
	return page, nil
}

// currentRecords are the records each current session declared, at most limit.
func (s *Store) currentRecords(ctx context.Context, current []Current, limit int) ([]protocol.AgentStatus, error) {
	var out []protocol.AgentStatus
	for _, c := range current {
		if len(out) == limit {
			break
		}
		st, err := s.AgentStatus(ctx, c.AgentID)
		if err != nil {
			return nil, err
		}
		if st != nil && st.SessionID == c.SessionID {
			out = append(out, *st)
		}
	}
	return out, nil
}

// fillRecords appends the others to first, skipping agents already present,
// up to limit.
func fillRecords(first, others []protocol.AgentStatus, limit int) []protocol.AgentStatus {
	seen := make(map[string]bool, len(first))
	for _, st := range first {
		seen[st.AgentID] = true
	}
	for _, st := range others {
		if len(first) == limit {
			break
		}
		if !seen[st.AgentID] {
			first = append(first, st)
		}
	}
	return first
}

func (s *Store) statusesByID(ctx context.Context, limit int) ([]protocol.AgentStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+statusColumns+` FROM agent_status ORDER BY agent_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read agent statuses: %w", err)
	}
	defer rows.Close()
	var out []protocol.AgentStatus
	for rows.Next() {
		st, err := scanStatus(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

const statusColumns = `agent_id, session_id, state, task, step, waiting_on, ref, declared_at`

func scanStatus(row scanner) (protocol.AgentStatus, error) {
	var st protocol.AgentStatus
	var declared string
	if err := row.Scan(&st.AgentID, &st.SessionID, &st.State, &st.Task, &st.Step, &st.WaitingOn, &st.Ref, &declared); err != nil {
		return st, err
	}
	at, err := time.Parse(time.RFC3339Nano, declared)
	if err != nil {
		return st, fmt.Errorf("agent status time: %w", err)
	}
	st.DeclaredAt = at
	return st, nil
}
