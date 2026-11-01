package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// peerRoutingSchema keeps what messages to and from agents need (#222):
// the agents this side shares with each contact, what each contact says it
// shares (a hint only), agent acknowledgments both ways, and the messages
// for this side's agents staged until the hub imports them.
const peerRoutingSchema = `
CREATE TABLE IF NOT EXISTS peer_grants (
    contact    TEXT NOT NULL,
    agent      TEXT NOT NULL,
    granted_at TEXT NOT NULL,
    PRIMARY KEY (contact, agent)
);
CREATE TABLE IF NOT EXISTS peer_directory (
    contact TEXT NOT NULL,
    agent   TEXT NOT NULL,
    seen_at TEXT NOT NULL,
    PRIMARY KEY (contact, agent)
);
CREATE TABLE IF NOT EXISTS peer_agent_reads (
    contact    TEXT NOT NULL,
    message_id TEXT NOT NULL,
    agent      TEXT NOT NULL,
    read_at    TEXT NOT NULL,
    state      TEXT NOT NULL,
    PRIMARY KEY (contact, message_id, agent)
);
CREATE TABLE IF NOT EXISTS peer_agent_acks (
    contact    TEXT NOT NULL,
    message_id TEXT NOT NULL,
    agent      TEXT NOT NULL,
    read_at    TEXT NOT NULL,
    PRIMARY KEY (contact, message_id, agent)
);
CREATE TABLE IF NOT EXISTS remote_imports (
    contact      TEXT NOT NULL,
    remote_id    TEXT NOT NULL,
    local_id     TEXT NOT NULL UNIQUE,
    sender       TEXT NOT NULL,
    recipients   TEXT NOT NULL,
    reply_id     TEXT NOT NULL,
    reply_author INTEGER NOT NULL,
    state        TEXT NOT NULL,
    staged_at    TEXT NOT NULL,
    settled_at   TEXT,
    PRIMARY KEY (contact, remote_id)
);
CREATE INDEX IF NOT EXISTS remote_imports_pending ON remote_imports (state, staged_at);
`

var _ messaging.RoutingStore = (*PeerMessages)(nil)

// ErrTooManyGrants is a share past messaging.MaxAgents agents for a contact.
var ErrTooManyGrants = fmt.Errorf("%w: a contact is shared at most %d agents", messaging.ErrInvalid, messaging.MaxAgents)

// shareAll is the agents argument that names every agent: registered ones
// for Share, a snapshot; every granted one for Unshare.
const shareAll = "*"

// Share grants contact's agents messages to agents, all at once or none,
// and answers what is shared now. "*" alone is every agent registered now:
// one registering later isn't shared.
func (p *PeerMessages) Share(ctx context.Context, contact string, agents []string, now time.Time) ([]string, error) {
	return p.grantChange(ctx, contact, func(tx *sql.Tx) error {
		named, err := namedAgents(ctx, tx, agents, everyAgent{query: `SELECT agent_id FROM durable_agents WHERE agent_id != 'operator'`})
		if err != nil {
			return err
		}
		for _, agent := range named {
			if _, err := tx.ExecContext(ctx, `INSERT INTO peer_grants (contact, agent, granted_at) VALUES (?, ?, ?)
ON CONFLICT DO NOTHING`, contact, agent, msgStamp(now)); err != nil {
				return err
			}
		}
		granted, err := grantedAgents(ctx, tx, contact)
		if err == nil && len(granted) > messaging.MaxAgents {
			err = ErrTooManyGrants
		}
		return err
	})
}

// Unshare revokes agents' grants, "*" alone every one, and answers what is
// still shared. A message staged for a group a revoked agent is in is
// withheld, the whole group, for good: a later Share doesn't revive it. A
// revoked agent's messages still queued to the contact are refused here.
func (p *PeerMessages) Unshare(ctx context.Context, contact string, agents []string, now time.Time) ([]string, error) {
	return p.grantChange(ctx, contact, func(tx *sql.Tx) error {
		named, err := namedAgents(ctx, tx, agents, everyAgent{`SELECT agent FROM peer_grants WHERE contact = ?`, []any{contact}})
		if err != nil {
			return err
		}
		for _, agent := range named {
			if _, err := tx.ExecContext(ctx, `DELETE FROM peer_grants WHERE contact = ? AND agent = ?`, contact, agent); err != nil {
				return err
			}
		}
		if err := withholdUngranted(ctx, tx, contact, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE peer_messages SET state = 'refused', class = ?2, reason = ?3
WHERE contact = ?1 AND direction = 'out' AND state = 'queued' AND from_agent IS NOT NULL
    AND from_agent NOT IN (SELECT agent FROM peer_grants WHERE contact = ?1)`,
			contact, messaging.Terminal, messaging.ReasonNotShared)
		return err
	})
}

// grantChange runs change on a confirmed contact's grants in one
// transaction and answers the grants after it.
func (p *PeerMessages) grantChange(ctx context.Context, contact string, change func(*sql.Tx) error) ([]string, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireContact(ctx, tx, contactKey(contact)); err != nil {
		return nil, err
	}
	if err := change(tx); err != nil {
		return nil, err
	}
	granted, err := grantedAgents(ctx, tx, contact)
	if err != nil {
		return nil, err
	}
	return granted, tx.Commit()
}

// everyAgent is what "*" names: the agents its query lists.
type everyAgent struct {
	query string
	args  []any
}

// namedAgents is the agents a grant change names: each valid, or "*" alone
// for every one.
func namedAgents(ctx context.Context, tx *sql.Tx, agents []string, all everyAgent) ([]string, error) {
	if len(agents) == 1 && agents[0] == shareAll {
		return queryStrings(ctx, tx, all.query, all.args...)
	}
	if len(agents) == 0 {
		return nil, fmt.Errorf("%w: no agents named", messaging.ErrInvalid)
	}
	for _, agent := range agents {
		if !messaging.ValidAgent(agent) {
			return nil, fmt.Errorf("%w: agent %q", messaging.ErrInvalid, agent)
		}
	}
	return agents, nil
}

// ungranted is an import from contact ?2 whose group holds an agent no
// longer shared with it.
const ungranted = `remote_imports.contact = ?2 AND COALESCE(remote_imports.kind, 'message') = 'message' AND EXISTS (SELECT 1 FROM json_each(remote_imports.recipients) j
    WHERE j.value NOT IN (SELECT agent FROM peer_grants WHERE contact = ?2))`

// withholdUngranted withholds, for the whole group, every message from
// contact to a group holding an agent no longer shared with it: staged ones
// are settled as withheld, and imported ones no recipient acknowledged yet
// leave the inboxes that still hold them, so no replay delivers them.
func withholdUngranted(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE remote_imports SET state = 'withheld', settled_at = ?1
WHERE state = 'pending' AND `+ungranted, msgStamp(now), contact)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM durable_inbox WHERE acknowledged_at IS NULL AND message_id IN (
    SELECT local_id FROM remote_imports WHERE state = 'imported' AND `+ungranted+`)`, msgStamp(now), contact)
	return err
}

// SharedAgents is the agents shared with contact, sorted.
func (p *PeerMessages) SharedAgents(ctx context.Context, contact string) ([]string, error) {
	var agents []string
	err := p.inTx(ctx, contact, func(tx *sql.Tx) (err error) {
		agents, err = grantedAgents(ctx, tx, contact)
		return err
	})
	return agents, err
}

func grantedAgents(ctx context.Context, q rowsQuerier, contact string) ([]string, error) {
	return queryStrings(ctx, q, `SELECT agent FROM peer_grants WHERE contact = ? ORDER BY agent`, contact)
}

// allGranted: every agent is shared with contact now.
func allGranted(ctx context.Context, q rowQuerier, contact string, agents []string) (bool, error) {
	for _, agent := range agents {
		var ok bool
		err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM peer_grants WHERE contact = ? AND agent = ?)`, contact, agent).Scan(&ok)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// PeerDirectory is the agents contact last said it shares, sorted; a hint
// for addressing, never a permission.
func (p *PeerMessages) PeerDirectory(ctx context.Context, contact string) ([]string, error) {
	return queryStrings(ctx, p.db, `SELECT agent FROM peer_directory WHERE contact = ? ORDER BY agent`, contact)
}

// SetPeerDirectory replaces what contact says it shares: at most MaxAgents
// valid agent IDs, or nothing changes.
func (p *PeerMessages) SetPeerDirectory(ctx context.Context, contact string, agents []string, at time.Time) error {
	if !validDirectory(agents) {
		return fmt.Errorf("%w: directory", messaging.ErrInvalid)
	}
	return p.inTx(ctx, contact, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM peer_directory WHERE contact = ?`, contact); err != nil {
			return err
		}
		for _, agent := range agents {
			if _, err := tx.ExecContext(ctx, `INSERT INTO peer_directory (contact, agent, seen_at) VALUES (?, ?, ?)
ON CONFLICT DO NOTHING`, contact, agent, msgStamp(at)); err != nil {
				return err
			}
		}
		return nil
	})
}

func validDirectory(agents []string) bool {
	return len(agents) <= messaging.MaxAgents && !slices.ContainsFunc(agents, func(a string) bool { return !messaging.ValidAgent(a) })
}

// PeerAgentRead records that one of contact's agents acknowledged a routed
// message this side sent it. An ID this side doesn't hold is answered and
// dropped; an agent the message wasn't to is ErrNotRecipient.
func (p *PeerMessages) PeerAgentRead(ctx context.Context, contact string, r messaging.AgentRead, at time.Time) error {
	return p.inTx(ctx, contact, func(tx *sql.Tx) error {
		return peerAgentReadIn(ctx, tx, agentReadAt{contact, r, at})
	})
}

// agentReadAt is one of the contact's agents acknowledging a message, at a
// time.
type agentReadAt struct {
	contact string
	r       messaging.AgentRead
	at      time.Time
}

// peerAgentReadIn is PeerAgentRead inside its transaction.
func peerAgentReadIn(ctx context.Context, tx *sql.Tx, a agentReadAt) error {
	contact, r, at := a.contact, a.r, a.at
	var to sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT to_agents FROM peer_messages WHERE contact = ? AND direction = 'out' AND message_id = ?`,
		contact, r.ID).Scan(&to)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	agents, err := groupList(to)
	if err != nil {
		return err
	}
	if !slices.Contains(agents, r.Agent) {
		return messaging.ErrNotRecipient
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO peer_agent_acks (contact, message_id, agent, read_at) VALUES (?, ?, ?, ?)
ON CONFLICT DO NOTHING`, contact, r.ID, r.Agent, msgStamp(at))
	return err
}

// QueueAgentRead queues, for the contact that sent it, a local agent's
// acknowledgment of an imported message, once; an agent it wasn't to is
// ErrNotRecipient. A local message that isn't an import is left alone.
func (p *PeerMessages) QueueAgentRead(ctx context.Context, localID, agent string, at time.Time) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := queueAgentReadIn(ctx, tx, agentRead{localID, agent, at}); err != nil {
		return err
	}
	return tx.Commit()
}

// agentRead is a local agent's acknowledgment of a local message.
type agentRead struct {
	localID, agent string
	at             time.Time
}

// queueAgentReadIn is QueueAgentRead inside a transaction, the agent's
// acknowledgment's own: it answers the contact the read is queued for, ""
// when the message isn't an import.
func queueAgentReadIn(ctx context.Context, tx *sql.Tx, r agentRead) (string, error) {
	var contact, remoteID, recipients string
	err := tx.QueryRowContext(ctx, `SELECT contact, remote_id, recipients FROM remote_imports WHERE local_id = ? AND state = 'imported'`,
		r.localID).Scan(&contact, &remoteID, &recipients)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	agents, err := groupList(sql.NullString{String: recipients, Valid: true})
	if err != nil {
		return "", err
	}
	if !slices.Contains(agents, r.agent) {
		return "", messaging.ErrNotRecipient
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_agent_reads (contact, message_id, agent, read_at, state) VALUES (?, ?, ?, ?, 'pending')
ON CONFLICT DO NOTHING`, contact, remoteID, r.agent, msgStamp(r.at)); err != nil {
		return "", err
	}
	// The oldest pending past the bound are dropped, as read receipts are.
	_, err = tx.ExecContext(ctx, `UPDATE peer_agent_reads SET state = 'dropped'
WHERE rowid IN (SELECT rowid FROM peer_agent_reads WHERE contact = ?1 AND state = 'pending'
    ORDER BY read_at DESC, rowid DESC LIMIT -1 OFFSET ?2)`, contact, messaging.Receipts)
	return contact, err
}

// PendingAgentReads is the oldest agent acknowledgments not yet confirmed
// by the contact.
func (p *PeerMessages) PendingAgentReads(ctx context.Context, contact string, max int) ([]messaging.AgentRead, error) {
	var out []messaging.AgentRead
	err := p.inTx(ctx, contact, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT message_id, agent FROM peer_agent_reads WHERE contact = ? AND state = 'pending'
ORDER BY read_at, rowid LIMIT ?`, contact, sessionBudget.clamp(budget{max, 0}).count)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r messaging.AgentRead
			if err := rows.Scan(&r.ID, &r.Agent); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// AgentReadConfirmed records the contact's AgentReadStored.
func (p *PeerMessages) AgentReadConfirmed(ctx context.Context, contact string, r messaging.AgentRead) error {
	return p.exec(ctx, contact, `UPDATE peer_agent_reads SET state = 'confirmed'
WHERE contact = ? AND message_id = ? AND agent = ? AND state = 'pending'`, contact, r.ID, r.Agent)
}

// queryStrings is one text column of every row a query returns.
func queryStrings(ctx context.Context, q rowsQuerier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
