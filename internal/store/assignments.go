package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Assignments (#278): an operator message to everyone or a room that tags
// agents assigns each its reply, stored with the message in one transaction
// and frozen by logical agent ID. For such a message, the reply as owner is
// the assignees' alone: claiming, releasing and finishing it move their own
// assignment, and nobody else can claim it. Every other task and role is a
// claim as before.

const assignmentsSchema = `
CREATE TABLE IF NOT EXISTS assignments (
    operator_message_id TEXT NOT NULL,
    agent_id            TEXT NOT NULL,
    task                TEXT NOT NULL,
    state               TEXT NOT NULL,
    assigned_at         TEXT NOT NULL,
    finished_at         TEXT,
    PRIMARY KEY (operator_message_id, agent_id)
);
`

const assignmentColumns = `operator_message_id, agent_id, task, state, assigned_at, finished_at`

// insertAssignments assigns the message's reply to each tagged agent, inside
// the transaction that stores the message.
func insertAssignments(ctx context.Context, tx *sql.Tx, messageID string, agents []string, at time.Time) error {
	for _, agent := range agents {
		if _, err := tx.ExecContext(ctx, `INSERT INTO assignments (`+assignmentColumns+`) VALUES (?, ?, ?, ?, ?, NULL)`,
			messageID, agent, protocol.TaskReply, protocol.AssignmentAssigned, stamp(at)); err != nil {
			return err
		}
	}
	return nil
}

// assignmentOp is what a claim frame does to its sender's assignment.
type assignmentOp int

const (
	assignClaim assignmentOp = iota
	assignRelease
	assignFinish
)

// assignmentNext is each operation's move from each state: the next state,
// or the refusal. The same state again is idempotent; nothing comes back
// from released or done.
var assignmentNext = map[assignmentOp]map[string]string{
	assignClaim:   {protocol.AssignmentAssigned: protocol.AssignmentAssigned},
	assignRelease: {protocol.AssignmentAssigned: protocol.AssignmentReleased, protocol.AssignmentReleased: protocol.AssignmentReleased},
	assignFinish:  {protocol.AssignmentAssigned: protocol.AssignmentDone, protocol.AssignmentDone: protocol.AssignmentDone},
}

// interceptsReply: a frame for the reply as owner on a message with
// assignments, which the assignments answer instead of the claims.
func interceptsReply(ctx context.Context, tx *sql.Tx, req ClaimRequest) (bool, error) {
	if req.Task != protocol.TaskReply || (req.Role != "" && req.Role != protocol.RoleOwner) {
		return false, nil
	}
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignments WHERE operator_message_id = ?`, req.MessageID).Scan(&n)
	return n > 0, err
}

// claimOrAssignment runs the claim change, or, for an assigned message's
// reply as owner, the assignment's.
func claimOrAssignment(ctx context.Context, tx *sql.Tx, req ClaimRequest, op assignmentOp, claim func() error) error {
	assigned, err := interceptsReply(ctx, tx, req)
	if err != nil {
		return err
	}
	if !assigned {
		return claim()
	}
	return moveAssignment(ctx, tx, req, op)
}

// moveAssignment moves the sender's own assignment. Someone not assigned
// can't claim it (a conflict that names the assignees) and holds nothing to
// release or finish.
func moveAssignment(ctx context.Context, tx *sql.Tx, req ClaimRequest, op assignmentOp) error {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM assignments WHERE operator_message_id = ? AND agent_id = ?`,
		req.MessageID, req.Agent).Scan(&state)
	switch {
	case errors.Is(err, sql.ErrNoRows) && op == assignClaim:
		return ErrClaimConflict
	case errors.Is(err, sql.ErrNoRows):
		return ErrClaimNotHeld
	case err != nil:
		return err
	}
	next, ok := assignmentNext[op][state]
	if !ok {
		return ErrClaimConflict
	}
	if next == state {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE assignments SET state = ?, finished_at = ? WHERE operator_message_id = ? AND agent_id = ?`,
		next, stamp(req.At), req.MessageID, req.Agent)
	return err
}

// messageAssignments is a message's assignments, in agent order, at most
// MaxMentions, the most a message can tag.
func messageAssignments(ctx context.Context, q rowsQuerier, messageID string) ([]protocol.Assignment, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+assignmentColumns+` FROM assignments WHERE operator_message_id = ? ORDER BY agent_id LIMIT ?`,
		messageID, protocol.MaxMentions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAssignment(row scanner) (protocol.Assignment, error) {
	var a protocol.Assignment
	var assigned string
	var finished sql.NullString
	if err := row.Scan(&a.OperatorMessageID, &a.AgentID, &a.Task, &a.State, &assigned, &finished); err != nil {
		return a, err
	}
	var err error
	if a.AssignedAt, err = time.Parse(time.RFC3339Nano, assigned); err != nil {
		return a, err
	}
	if finished.Valid {
		at, err := time.Parse(time.RFC3339Nano, finished.String)
		if err != nil {
			return a, err
		}
		a.FinishedAt = &at
	}
	return a, nil
}

// withAssignments adds a message's assignments to its claim set, with
// their own total and whether the page holds them all.
func withAssignments(ctx context.Context, q interface {
	rowsQuerier
	rowQuerier
}, set protocol.ClaimSet) (protocol.ClaimSet, error) {
	page := &protocol.AssignmentPage{Assignments: []protocol.Assignment{}}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignments WHERE operator_message_id = ?`, set.OperatorMessageID).
		Scan(&page.AssignmentsTotal); err != nil {
		return set, err
	}
	assignments, err := messageAssignments(ctx, q, set.OperatorMessageID)
	if err != nil {
		return set, err
	}
	page.Assignments = append(page.Assignments, assignments...)
	page.AssignmentsTruncated = page.AssignmentsTotal > len(page.Assignments)
	set.AssignmentPage = page
	return set, nil
}

// AgentAssignment is the agent's assignment of a message, nil when it has
// none: what its frame carries, on delivery and on replay.
func agentAssignment(ctx context.Context, q rowQuerier, messageID, agent string) (*protocol.AssignmentView, error) {
	var view protocol.AssignmentView
	err := q.QueryRowContext(ctx, `SELECT task, state FROM assignments WHERE operator_message_id = ? AND agent_id = ?`, messageID, agent).
		Scan(&view.Task, &view.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &view, nil
}

// AssignedAudience is where a reply to an assigned message belongs: assigned
// reports whether the message assigns anyone, and room is its room ("" for
// everyone). A message that assigns nobody keeps today's reply rules.
func (s *Store) AssignedAudience(ctx context.Context, messageID string) (assigned bool, room string, err error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignments WHERE operator_message_id = ?`, messageID).Scan(&n); err != nil || n == 0 {
		return false, "", err
	}
	var stored sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT room FROM message_metadata WHERE message_id = ?`, messageID).Scan(&stored)
	return true, stored.String, err
}
