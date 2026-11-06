package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// peerAgentReadsColumns is peer_agent_reads with pairing_epoch in its key (#276).
const peerAgentReadsColumns = `(
    contact    TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    message_id TEXT NOT NULL,
    agent      TEXT NOT NULL,
    read_at    TEXT NOT NULL,
    state      TEXT NOT NULL,
    PRIMARY KEY (contact, pairing_epoch, message_id, agent)
)`

// peerAgentAcksColumns is peer_agent_acks with pairing_epoch in its key (#276).
const peerAgentAcksColumns = `(
    contact    TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    message_id TEXT NOT NULL,
    agent      TEXT NOT NULL,
    read_at    TEXT NOT NULL,
    PRIMARY KEY (contact, pairing_epoch, message_id, agent)
)`

// remoteImportsColumns is remote_imports with pairing_epoch in its key (#276).
const remoteImportsColumns = `(
    contact      TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    remote_id    TEXT NOT NULL,
    local_id     TEXT NOT NULL UNIQUE,
    sender       TEXT NOT NULL,
    recipients   TEXT NOT NULL,
    reply_id     TEXT NOT NULL,
    reply_author INTEGER NOT NULL,
    state        TEXT NOT NULL,
    staged_at    TEXT NOT NULL,
    settled_at   TEXT,
    PRIMARY KEY (contact, pairing_epoch, remote_id)
)`

// The routing tables keep what messages to and from agents need (#222):
// the agents this side shares with each contact, what each contact says it
// shares (a hint only), agent acknowledgments both ways, and the messages
// for this side's agents staged until the hub imports them. The transition
// creates each, or rebuilds a released one, with pairing_epoch in its key
// (#276).

// peerGrantsColumns is peer_grants with pairing_epoch in its key (#276).
const peerGrantsColumns = `(
    contact    TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    agent      TEXT NOT NULL,
    granted_at TEXT NOT NULL,
    PRIMARY KEY (contact, pairing_epoch, agent)
)`

// peerDirectoryColumns is peer_directory with pairing_epoch in its key (#276).
const peerDirectoryColumns = `(
    contact TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    agent   TEXT NOT NULL,
    seen_at TEXT NOT NULL,
    PRIMARY KEY (contact, pairing_epoch, agent)
)`

var _ messaging.RoutingStore = (*PeerMessages)(nil)

// ErrTooManyGrants is a share past messaging.MaxAgents agents for a contact.
var ErrTooManyGrants = fmt.Errorf("%w: a contact is shared at most %d agents", messaging.ErrInvalid, messaging.MaxAgents)

// shareAll is the agents argument that names every agent: registered ones
// for Share, a snapshot; every granted one for Unshare.
const shareAll = "*"

// Share grants contact's agents messages to agents, all at once or none,
// in its current pairing, and answers what is shared now. "*" alone is
// every agent registered now: one registering later isn't shared.
func (p *PeerMessages) Share(ctx context.Context, contact string, agents []string, now time.Time) ([]string, error) {
	return p.grantChange(ctx, contact, func(tx *sql.Tx) error {
		named, err := namedAgents(ctx, tx, agents, everyAgent{query: `SELECT agent_id FROM durable_agents WHERE agent_id != 'operator'`})
		if err != nil {
			return err
		}
		for _, agent := range named {
			if _, err := tx.ExecContext(ctx, `INSERT INTO peer_grants (contact, pairing_epoch, agent, granted_at) VALUES (?1, `+currentEpochOf1+`, ?2, ?3)
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

// Unshare revokes agents' grants in the contact's current pairing, "*"
// alone every one, and answers what is still shared. A message staged for a group a revoked agent is in is
// withheld, the whole group, for good: a later Share doesn't revive it. A
// revoked agent's messages still queued to the contact are refused here.
func (p *PeerMessages) Unshare(ctx context.Context, contact string, agents []string, now time.Time) ([]string, error) {
	return p.grantChange(ctx, contact, func(tx *sql.Tx) error {
		named, err := namedAgents(ctx, tx, agents, everyAgent{`SELECT agent FROM peer_grants WHERE contact = ?1 AND pairing_epoch = ` + currentEpochOf1,
			[]any{contact}})
		if err != nil {
			return err
		}
		for _, agent := range named {
			if _, err := tx.ExecContext(ctx, `DELETE FROM peer_grants WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND agent = ?2`,
				contact, agent); err != nil {
				return err
			}
		}
		// A revoke is an explicit decision: it never reads as undecided (#293).
		if err := denyScopes(ctx, tx, denial{contact, named, []string{protocol.ConsentMessage}, now}); err != nil {
			return err
		}
		if err := withholdUngranted(ctx, tx, contact, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE peer_messages SET state = 'refused', class = ?2, reason = ?3
WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND direction = 'out' AND state = 'queued' AND from_agent IS NOT NULL
    AND from_agent NOT IN (SELECT agent FROM peer_grants WHERE contact = ?1 AND pairing_epoch = peer_messages.pairing_epoch)`,
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

// ungranted is an import from contact ?2, in its current pairing, whose
// group holds an agent no longer shared with it there. One the operator
// allowed from a hold (#293) needs no share: only an agent's explicit
// denial since withholds it.
var ungranted = `remote_imports.contact = ?2 AND remote_imports.pairing_epoch = ` + currentEpochAt(2) + `
    AND COALESCE(remote_imports.kind, 'message') = 'message' AND EXISTS (SELECT 1 FROM json_each(remote_imports.recipients) j
    WHERE j.value NOT IN (SELECT agent FROM peer_grants WHERE contact = ?2 AND pairing_epoch = remote_imports.pairing_epoch)
    AND (NOT ` + consentAllowedImport + ` OR j.value IN (SELECT agent FROM consent_decisions
        WHERE contact = ?2 AND pairing_epoch = remote_imports.pairing_epoch AND operation = 'message' AND state = 'denied')))`

// consentAllowedImport: the import was staged by the operator allowing
// its hold once or for a session (#293), in the import's pairing.
const consentAllowedImport = `EXISTS (SELECT 1 FROM consent_requests cr WHERE cr.contact = remote_imports.contact
    AND cr.pairing_epoch = remote_imports.pairing_epoch AND cr.message_id = remote_imports.remote_id
    AND cr.evidence_kind IN ('once', 'session'))`

// withholdUngranted withholds, for the whole group, every message from
// contact to a group holding an agent no longer shared with it: staged ones
// are settled as withheld, and imported ones no recipient acknowledged yet
// leave the inboxes that still hold them, so no replay delivers them.
func withholdUngranted(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE remote_imports SET state = 'withheld', reason = 'revoked', settled_at = ?1
WHERE state = 'pending' AND `+ungranted, msgStamp(now), contact)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM durable_inbox WHERE acknowledged_at IS NULL AND message_id IN (
    SELECT local_id FROM remote_imports WHERE state = 'imported' AND `+ungranted+`)`, msgStamp(now), contact)
	if err != nil {
		return err
	}
	return settleConsentOutcomes(ctx, tx, contact) // an allowed hold withheld is told so (#293)
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

// grantedAgents is the agents shared with contact in its current pairing:
// an earlier pairing's grants never authorize this one (#276).
func grantedAgents(ctx context.Context, q rowsQuerier, contact string) ([]string, error) {
	return queryStrings(ctx, q, `SELECT agent FROM peer_grants WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` ORDER BY agent`, contact)
}

// allGranted: every agent is shared with contact now, in its current
// pairing.
func allGranted(ctx context.Context, q rowQuerier, contact string, agents []string) (bool, error) {
	for _, agent := range agents {
		var ok bool
		err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM peer_grants WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+`
    AND agent = ?2)`, contact, agent).Scan(&ok)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// PeerDirectory is the agents contact last said it shares in its current
// pairing, sorted; a hint for addressing, never a permission. A session
// the contact's pairing has moved past reads nothing (#276).
func (p *PeerMessages) PeerDirectory(ctx context.Context, contact string) ([]string, error) {
	var agents []string
	err := p.inTx(ctx, contact, func(tx *sql.Tx) (err error) {
		agents, err = storedDirectory(ctx, tx, contact)
		return err
	})
	return agents, err
}

// storedDirectory is the directory stored for the contact's current
// pairing, sorted: empty after a new pairing until the contact sends one.
func storedDirectory(ctx context.Context, q rowsQuerier, contact string) ([]string, error) {
	return queryStrings(ctx, q, `SELECT agent FROM peer_directory WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` ORDER BY agent`, contact)
}

// SetPeerDirectory replaces what contact says it shares: at most MaxAgents
// valid agent IDs, or nothing changes.
func (p *PeerMessages) SetPeerDirectory(ctx context.Context, contact string, agents []string, at time.Time) error {
	_, err := p.ReplacePeerDirectory(ctx, contact, agents, at)
	return err
}

// ReplacePeerDirectory is SetPeerDirectory, saying whether the agents
// listed differ from those before (#296).
func (p *PeerMessages) ReplacePeerDirectory(ctx context.Context, contact string, agents []string, at time.Time) (bool, error) {
	if !validDirectory(agents) {
		return false, fmt.Errorf("%w: directory", messaging.ErrInvalid)
	}
	var changed bool
	err := p.inTx(ctx, contact, func(tx *sql.Tx) (err error) {
		if err := directorySeen(ctx, tx, contact, at); err != nil {
			return err
		}
		changed, err = directoryDiffers(ctx, tx, contact, agents)
		if err != nil || !changed {
			return err // the same agents: nothing more to write
		}
		return writeDirectory(ctx, tx, peerDirectory{contact, agents, at})
	})
	return changed && err == nil, err
}

// directorySeen records when the contact's directory was last received,
// so an empty one is told from none (#304).
func directorySeen(ctx context.Context, tx *sql.Tx, contact string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET directory_at = ? WHERE identity_key = ?`, msgStamp(at), contact)
	return err
}

// directoryDiffers: the agents listed aren't the ones stored.
func directoryDiffers(ctx context.Context, tx *sql.Tx, contact string, agents []string) (bool, error) {
	before, err := storedDirectory(ctx, tx, contact)
	if err != nil {
		return false, err
	}
	after := slices.Clone(agents)
	slices.Sort(after)
	return !slices.Equal(before, slices.Compact(after)), nil
}

// peerDirectory is what a contact says it shares, as received at.
type peerDirectory struct {
	contact string
	agents  []string
	at      time.Time
}

// writeDirectory replaces the stored directory of the contact's current
// pairing; an earlier pairing's stays as it was.
func writeDirectory(ctx context.Context, tx *sql.Tx, d peerDirectory) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM peer_directory WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1, d.contact); err != nil {
		return err
	}
	for _, agent := range d.agents {
		if _, err := tx.ExecContext(ctx, `INSERT INTO peer_directory (contact, pairing_epoch, agent, seen_at) VALUES (?1, `+currentEpochOf1+`, ?2, ?3)
ON CONFLICT DO NOTHING`, d.contact, agent, msgStamp(d.at)); err != nil {
			return err
		}
	}
	return nil
}

func validDirectory(agents []string) bool {
	return len(agents) <= messaging.MaxAgents && !slices.ContainsFunc(agents, func(a string) bool { return !messaging.ValidAgent(a) })
}

// PeerAgentRead records that one of contact's agents acknowledged a routed
// message this side sent it, in the contact's current pairing. An ID this
// side doesn't hold there is answered and dropped; an agent the message
// wasn't to is ErrNotRecipient.
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
	err := tx.QueryRowContext(ctx, `SELECT to_agents FROM peer_messages WHERE `+currentOutbound, contact, r.ID).Scan(&to)
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
	_, err = tx.ExecContext(ctx, `INSERT INTO peer_agent_acks (contact, pairing_epoch, message_id, agent, read_at)
VALUES (?1, `+currentEpochOf1+`, ?2, ?3, ?4) ON CONFLICT DO NOTHING`, contact, r.ID, r.Agent, msgStamp(at))
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
// when the message isn't an import of the contact's current pairing. An
// earlier pairing's import owes the contact nothing now (#276).
func queueAgentReadIn(ctx context.Context, tx *sql.Tx, r agentRead) (string, error) {
	var contact, remoteID, recipients string
	err := tx.QueryRowContext(ctx, `SELECT ri.contact, ri.remote_id, ri.recipients FROM remote_imports ri
WHERE ri.local_id = ? AND ri.state = 'imported'
    AND ri.pairing_epoch = COALESCE((SELECT ce.pairing_epoch FROM peer_contacts ce WHERE ce.identity_key = ri.contact), 1)`,
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
	return contact, pendAgentRead(ctx, tx, agentReadAt{contact, messaging.AgentRead{ID: remoteID, Agent: r.agent}, r.at})
}

// pendAgentRead queues an agent's read for the contact in its current
// pairing; the oldest pending past the bound are dropped, as read receipts
// are.
func pendAgentRead(ctx context.Context, tx *sql.Tx, a agentReadAt) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_agent_reads (contact, pairing_epoch, message_id, agent, read_at, state)
VALUES (?1, `+currentEpochOf1+`, ?2, ?3, ?4, 'pending') ON CONFLICT DO NOTHING`, a.contact, a.r.ID, a.r.Agent, msgStamp(a.at)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE peer_agent_reads SET state = 'dropped'
WHERE rowid IN (SELECT rowid FROM peer_agent_reads WHERE `+currentAgentReads+` AND state = 'pending'
    ORDER BY read_at DESC, rowid DESC LIMIT -1 OFFSET ?2)`, a.contact, messaging.Receipts)
	return err
}

// currentAgentReads is the agent reads owed contact ?1 in its current
// pairing (#276).
const currentAgentReads = `contact = ?1 AND pairing_epoch = ` + currentEpochOf1

// PendingAgentReads is the oldest agent acknowledgments, in the contact's
// current pairing, not yet confirmed by it.
func (p *PeerMessages) PendingAgentReads(ctx context.Context, contact string, max int) ([]messaging.AgentRead, error) {
	var out []messaging.AgentRead
	err := p.inTx(ctx, contact, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT message_id, agent FROM peer_agent_reads WHERE `+currentAgentReads+` AND state = 'pending'
ORDER BY read_at, rowid LIMIT ?2`, contact, sessionBudget.clamp(budget{max, 0}).count)
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

// AgentReadConfirmed records the contact's AgentReadStored, for a read of
// its current pairing.
func (p *PeerMessages) AgentReadConfirmed(ctx context.Context, contact string, r messaging.AgentRead) error {
	return p.exec(ctx, contact, `UPDATE peer_agent_reads SET state = 'confirmed'
WHERE `+currentAgentReads+` AND message_id = ?2 AND agent = ?3 AND state = 'pending'`, contact, r.ID, r.Agent)
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
