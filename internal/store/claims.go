package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// claimsSchema records task claims on operator messages (#141). The partial
// unique index makes the first claim win atomically: at most one active or
// done claim per message, task and role. Released rows stay as history.
const claimsSchema = `
CREATE TABLE IF NOT EXISTS claims (
    operator_message_id TEXT NOT NULL,
    task                TEXT NOT NULL,
    role                TEXT NOT NULL,
    agent_id            TEXT NOT NULL,
    topic               TEXT NOT NULL,
    state               TEXT NOT NULL,
    claimed_at          TEXT NOT NULL,
    finished_at         TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS claims_held ON claims(operator_message_id, task, role) WHERE state != 'released';
CREATE INDEX IF NOT EXISTS claims_by_message ON claims(operator_message_id, claimed_at);
`

// Claim outcomes the hub maps to protocol errors.
var (
	ErrClaimTarget    = errors.New("operator_message_id does not name an operator message delivered to this agent")
	ErrClaimConflict  = errors.New("claimed by another agent, or done")
	ErrClaimNotHeld   = errors.New("no matching claim held by this agent")
	ErrClaimBothRoles = errors.New("an agent can't be both owner and reviewer of one task")
	ErrClaimsFull     = errors.New("this operator message has the maximum number of claims")
)

// ClaimRequest is one validated claim frame, with the broker-set identity.
type ClaimRequest struct {
	MessageID, Task, Topic, Role, Agent string
	At                                  time.Time
}

// SetClaim records a claim, or reports the holder. It returns the message's
// claims after the change, or, on a conflict, with the holder.
func (s *Store) SetClaim(ctx context.Context, req ClaimRequest) (protocol.ClaimSet, error) {
	return s.claimTx(ctx, req.MessageID, req.Agent, func(tx *sql.Tx) error { return setClaim(ctx, tx, req) })
}

// ReleaseClaim frees the agent's active claim; the slot becomes free again.
func (s *Store) ReleaseClaim(ctx context.Context, req ClaimRequest) (protocol.ClaimSet, error) {
	return s.claimTx(ctx, req.MessageID, req.Agent, func(tx *sql.Tx) error {
		return finishClaim(ctx, tx, req, protocol.ClaimReleased)
	})
}

// FinishClaim marks the agent's active owner claim done: terminal, and the
// task never becomes free again.
func (s *Store) FinishClaim(ctx context.Context, req ClaimRequest) (protocol.ClaimSet, error) {
	req.Role = protocol.RoleOwner
	return s.claimTx(ctx, req.MessageID, req.Agent, func(tx *sql.Tx) error {
		return finishClaim(ctx, tx, req, protocol.ClaimDone)
	})
}

// Claims reads a message's claims, if the agent may see that message.
func (s *Store) Claims(ctx context.Context, messageID, agent string) (protocol.ClaimSet, error) {
	return s.claimTx(ctx, messageID, agent, func(*sql.Tx) error { return nil })
}

// claimTx checks the target, runs change, and reads the message's claims, in
// one transaction; nothing is visible unless it commits. A conflict commits
// nothing and still returns the claims naming the holder.
func (s *Store) claimTx(ctx context.Context, messageID, agent string, change func(*sql.Tx) error) (protocol.ClaimSet, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.ClaimSet{}, err
	}
	defer tx.Rollback()
	if err := claimTarget(ctx, tx, messageID, agent); err != nil {
		return protocol.ClaimSet{}, err
	}
	changeErr := change(tx)
	set, err := messageClaims(ctx, tx, messageID)
	if err != nil {
		return protocol.ClaimSet{}, err
	}
	if changeErr != nil {
		return set, changeErr
	}
	return set, tx.Commit()
}

// claimTarget requires an operator-origin message whose frozen recipients
// include the agent. Every other case reads the same, so a claim never reveals
// whether a message exists.
func claimTarget(ctx context.Context, tx *sql.Tx, messageID, agent string) error {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_metadata mm JOIN message_receipts r USING(message_id)
WHERE mm.message_id = ? AND mm.origin = 'operator' AND r.agent_id = ?`, messageID, agent).Scan(&found)
	if err != nil {
		return err
	}
	if found == 0 {
		return ErrClaimTarget
	}
	return nil
}

func setClaim(ctx context.Context, tx *sql.Tx, req ClaimRequest) error {
	holder, done, err := heldBy(ctx, tx, req.MessageID, req.Task, req.Role)
	switch {
	case err != nil:
		return err
	case done:
		return ErrClaimConflict
	case holder == req.Agent:
		_, err = tx.ExecContext(ctx, `UPDATE claims SET topic = ? WHERE operator_message_id = ? AND task = ? AND role = ? AND state = 'active' AND agent_id = ?`,
			req.Topic, req.MessageID, req.Task, req.Role, req.Agent)
		return err
	case holder != "":
		return ErrClaimConflict
	}
	if err := checkNewClaim(ctx, tx, req); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO claims (operator_message_id, task, role, agent_id, topic, state, claimed_at) VALUES (?, ?, ?, ?, ?, 'active', ?)`,
		req.MessageID, req.Task, req.Role, req.Agent, req.Topic, stamp(req.At))
	return err
}

// heldBy is the agent holding the slot ("" when free) and whether the owner
// finished it, which makes the slot terminal.
func heldBy(ctx context.Context, tx *sql.Tx, messageID, task, role string) (string, bool, error) {
	var agent, state string
	err := tx.QueryRowContext(ctx, `SELECT agent_id, state FROM claims WHERE operator_message_id = ? AND task = ? AND role = ? AND state != 'released'`,
		messageID, task, role).Scan(&agent, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return agent, state == protocol.ClaimDone, err
}

// checkNewClaim enforces one role per agent and task, and the per-message bound.
func checkNewClaim(ctx context.Context, tx *sql.Tx, req ClaimRequest) error {
	var otherRole, held int
	err := tx.QueryRowContext(ctx, `SELECT
  COALESCE(SUM(task = ? AND agent_id = ? AND role != ? AND state != 'released'), 0),
  COALESCE(SUM(state != 'released'), 0)
FROM claims WHERE operator_message_id = ?`, req.Task, req.Agent, req.Role, req.MessageID).Scan(&otherRole, &held)
	if err != nil {
		return err
	}
	if otherRole > 0 {
		return ErrClaimBothRoles
	}
	if held >= protocol.ClaimsPerMessage {
		return ErrClaimsFull
	}
	return nil
}

func finishClaim(ctx context.Context, tx *sql.Tx, req ClaimRequest, state string) error {
	res, err := tx.ExecContext(ctx, `UPDATE claims SET state = ?, finished_at = ? WHERE operator_message_id = ? AND task = ? AND role = ? AND agent_id = ? AND state = 'active'`,
		state, stamp(req.At), req.MessageID, req.Task, req.Role, req.Agent)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errors.Join(err, ErrClaimNotHeld)
	}
	return nil
}

// messageClaims is a message's claims, held ones first, at most the bound,
// with the total.
func messageClaims(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, messageID string) (protocol.ClaimSet, error) {
	set := protocol.ClaimSet{OperatorMessageID: messageID, ClaimPage: protocol.ClaimPage{Claims: []protocol.Claim{}}}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM claims WHERE operator_message_id = ?`, messageID).Scan(&set.Total); err != nil {
		return set, err
	}
	rows, err := q.QueryContext(ctx, `SELECT `+claimColumns+` FROM claims WHERE operator_message_id = ?
ORDER BY state = 'released', claimed_at, task, role LIMIT ?`, messageID, protocol.ClaimsPerMessage)
	if err != nil {
		return set, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return set, err
		}
		set.Claims = append(set.Claims, c)
	}
	set.Truncated = set.Total > len(set.Claims)
	return set, rows.Err()
}

// RecentClaims is the claims of the newest operator messages that have any,
// at most limit messages, with the number of such messages.
func (s *Store) RecentClaims(ctx context.Context, limit int) ([]protocol.ClaimSet, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT operator_message_id) FROM claims`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT operator_message_id FROM claims GROUP BY operator_message_id ORDER BY MAX(claimed_at) DESC LIMIT ?`, limit)
	if err != nil {
		return nil, 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	sets := make([]protocol.ClaimSet, 0, len(ids))
	for _, id := range ids {
		set, err := messageClaims(ctx, s.db, id)
		if err != nil {
			return nil, 0, err
		}
		sets = append(sets, set)
	}
	return sets, total, nil
}

const claimColumns = `operator_message_id, task, topic, role, agent_id, state, claimed_at, finished_at`

func scanClaim(rows *sql.Rows) (protocol.Claim, error) {
	var c protocol.Claim
	var claimed string
	var finished sql.NullString
	if err := rows.Scan(&c.OperatorMessageID, &c.Task, &c.Topic, &c.Role, &c.AgentID, &c.State, &claimed, &finished); err != nil {
		return c, err
	}
	at, err := time.Parse(time.RFC3339Nano, claimed)
	if err != nil {
		return c, fmt.Errorf("claim time: %w", err)
	}
	c.ClaimedAt = at
	if finished.Valid {
		f, err := time.Parse(time.RFC3339Nano, finished.String)
		if err != nil {
			return c, fmt.Errorf("claim finish time: %w", err)
		}
		c.FinishedAt = &f
	}
	return c, nil
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
