package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Claims on a message from a paired workspace (#419). The agent that
// received the message claims a task of it exactly as it claims one of an
// operator message: the same table, the same slots and bounds, keyed by
// the message's ID here. What differs is that the claim's state is also
// owed to the workspace that sent the message, which shows it beside its
// own copy. A claim coordinates; neither side refuses anything because of
// one.
//
// claim_notices_out is what this side owes: one row per claim, holding its
// latest state and how many states it has had (seq), pending until the
// other side answered that state. remote_claims is what this side was
// told about messages it sent. Both are scoped to the pairing, so a notice
// queued before a re-pair never reaches the new pairing, and a message ID
// reused under it never meets an old claim.
const pairedClaimsSchema = `
CREATE TABLE IF NOT EXISTS claim_notices_out (
    contact       TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL,
    remote_id     TEXT NOT NULL,
    agent         TEXT NOT NULL,
    role          TEXT NOT NULL,
    task          TEXT NOT NULL,
    state         TEXT NOT NULL,
    seq           INTEGER NOT NULL,
    at            TEXT NOT NULL,
    pending       INTEGER NOT NULL DEFAULT 1,
    dropped       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (contact, pairing_epoch, remote_id, agent, role, task)
);
CREATE INDEX IF NOT EXISTS claim_notices_pending ON claim_notices_out(contact, pairing_epoch, pending, at);
CREATE TABLE IF NOT EXISTS remote_claims (
    contact       TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL,
    message_id    TEXT NOT NULL,
    agent         TEXT NOT NULL,
    role          TEXT NOT NULL,
    task          TEXT NOT NULL,
    state         TEXT NOT NULL,
    seq           INTEGER NOT NULL,
    at            TEXT NOT NULL,
    received_at   TEXT NOT NULL,
    PRIMARY KEY (contact, pairing_epoch, message_id, agent, role, task)
);
`

var _ messaging.ClaimStore = (*PeerMessages)(nil)

// PairedClaim is a paired message's claims after a request, and for a
// change whether it was queued to be shown to the message's sender, with
// the reason code when it wasn't.
type PairedClaim struct {
	Set     protocol.ClaimSet
	Changed bool // a change, so Visible and Reason mean something
	Visible bool
	Reason  string
	Contact string // the workspace owed a notice, when Visible
}

// PairedClaim serves a claim frame that names a paired message. A message
// this agent received from a paired workspace is claimed, released,
// finished or read like an operator message's; a message this agent sent
// one can only be read, for the claims the other workspace told of it.
// Anything else is ErrClaimTarget, whatever the reason.
func (s *Store) PairedClaim(ctx context.Context, kind string, req ClaimRequest) (PairedClaim, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PairedClaim{}, err
	}
	defer tx.Rollback()
	imp, received, err := receivedBy(ctx, tx, req.MessageID, req.Agent)
	if err != nil {
		return PairedClaim{}, err
	}
	if !received {
		return sentClaims(ctx, tx, kind, req)
	}
	out, changeErr := claimReceived(ctx, tx, kind, req, imp)
	set, err := messageClaims(ctx, tx, req.MessageID)
	if err != nil {
		return PairedClaim{}, err
	}
	out.Set = pairedSet(set, req.MessageID)
	if changeErr != nil {
		return out, changeErr // a conflict names the holder and commits nothing
	}
	return out, tx.Commit()
}

// receivedBy is the imported paired message with this ID here, when the
// agent is one of its recipients as frozen when it arrived.
func receivedBy(ctx context.Context, tx *sql.Tx, localID, agent string) (RemoteImport, bool, error) {
	imp, state, err := scanImport(tx.QueryRowContext(ctx, `SELECT `+importColumns+importFrom+` WHERE ri.local_id = ?`, localID))
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteImport{}, false, nil
	}
	if err != nil {
		return RemoteImport{}, false, err
	}
	return imp, state == ImportImported && slices.Contains(imp.Recipients, agent), nil
}

// claimReceived applies a claim frame to a received message: the local
// change, and in the same transaction the notice it owes the sender.
func claimReceived(ctx context.Context, tx *sql.Tx, kind string, req ClaimRequest, imp RemoteImport) (PairedClaim, error) {
	state, err := changeClaim(ctx, tx, kind, req)
	if err != nil || state == "" {
		return PairedClaim{}, err
	}
	out := PairedClaim{Changed: true}
	out.Reason, err = hiddenFrom(ctx, tx, imp, req.Agent)
	if err != nil || out.Reason != "" {
		return out, err
	}
	out.Visible, out.Contact = true, imp.Contact
	return out, queueClaimNotice(ctx, tx, imp, req, state)
}

// changeClaim runs the frame's change and names the state it left the
// claim in; "" for a read.
func changeClaim(ctx context.Context, tx *sql.Tx, kind string, req ClaimRequest) (string, error) {
	switch kind {
	case protocol.TypeClaimSet:
		return protocol.ClaimActive, setClaim(ctx, tx, req)
	case protocol.TypeClaimRelease:
		return protocol.ClaimReleased, finishClaim(ctx, tx, req, protocol.ClaimReleased)
	case protocol.TypeClaimDone:
		if req.Role == "" {
			req.Role = protocol.RoleOwner
		}
		return protocol.ClaimDone, finishClaim(ctx, tx, req, protocol.ClaimDone)
	}
	return "", nil
}

// hiddenFrom is why a claim on imp by agent isn't shown to its sender, or
// "": the pairing it arrived under has ended, the agent isn't shared with
// that workspace, or that workspace last spoke a version without claims.
func hiddenFrom(ctx context.Context, tx *sql.Tx, imp RemoteImport, agent string) (string, error) {
	var current, shared bool
	var version sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT
    COALESCE((SELECT c.unpaired_at = '' AND c.pairing_epoch = ?2 FROM peer_contacts c WHERE c.identity_key = ?1), 0),
    EXISTS (SELECT 1 FROM peer_grants g WHERE g.contact = ?1 AND g.pairing_epoch = ?2 AND g.agent = ?3),
    (SELECT c.peer_version FROM peer_contacts c WHERE c.identity_key = ?1)`,
		imp.Contact, imp.source().epoch, agent).Scan(&current, &shared, &version)
	switch {
	case err != nil:
		return "", err
	case !current:
		return protocol.ClaimUnpaired, nil
	case !shared:
		return protocol.ClaimNotShared, nil
	case version.Valid && version.Int64 > 0 && version.Int64 < int64(messaging.ClaimVersion):
		return protocol.ClaimUnsupported, nil
	}
	return "", nil
}

// queueClaimNotice records the claim's new state as owed to the sender:
// the next sequence number of that claim, pending again.
func queueClaimNotice(ctx context.Context, tx *sql.Tx, imp RemoteImport, req ClaimRequest, state string) error {
	role := req.Role
	if role == "" {
		role = protocol.RoleOwner
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO claim_notices_out (contact, pairing_epoch, remote_id, agent, role, task, state, seq, at)
VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?)
ON CONFLICT (contact, pairing_epoch, remote_id, agent, role, task)
DO UPDATE SET state = excluded.state, seq = seq + 1, at = excluded.at, pending = 1, dropped = ''`,
		imp.Contact, imp.source().epoch, imp.RemoteID, req.Agent, role, req.Task, state, stamp(req.At))
	return err
}

// pairedSet is a set of local claims on a paired message as a frame
// carries them: named by message_id, never as an operator message's.
func pairedSet(set protocol.ClaimSet, messageID string) protocol.ClaimSet {
	set.OperatorMessageID = ""
	set.AssignmentPage = nil
	for i := range set.Claims {
		set.Claims[i].OperatorMessageID, set.Claims[i].MessageID = "", messageID
	}
	return set
}

// sentClaims answers a frame naming a message this agent sent a paired
// workspace: a read returns what that workspace told of it; a change is
// refused, since only a recipient claims.
func sentClaims(ctx context.Context, tx *sql.Tx, kind string, req ClaimRequest) (PairedClaim, error) {
	if kind != protocol.TypeClaimGet {
		return PairedClaim{}, ErrClaimTarget
	}
	set, err := sentMessageClaims(ctx, tx, req.MessageID, req.Agent)
	return PairedClaim{Set: set}, err
}

// SentMessageClaims is the claims a paired workspace told of a message
// this side sent it, for the agent that sent it ("" for the operator).
// Each names the message by message_id and the workspace it came from.
func (s *Store) SentMessageClaims(ctx context.Context, messageID, agent string) (protocol.ClaimSet, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return protocol.ClaimSet{}, err
	}
	defer tx.Rollback()
	return sentMessageClaims(ctx, tx, messageID, agent)
}

// sentByAgent selects the outbound rows of a message by the ID its sender knows
// it by (the agent's own, or the one it travels under), sent by that agent.
const sentByAgent = `pm.direction = 'out' AND (pm.local_id = ?1 OR pm.message_id = ?1) AND COALESCE(pm.from_agent, '') = ?2`

func sentMessageClaims(ctx context.Context, tx *sql.Tx, messageID, agent string) (protocol.ClaimSet, error) {
	set := protocol.ClaimSet{ClaimPage: protocol.ClaimPage{Claims: []protocol.Claim{}}}
	var sent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_messages pm WHERE `+sentByAgent, messageID, agent).Scan(&sent); err != nil {
		return set, err
	}
	if sent == 0 {
		return set, ErrClaimTarget
	}
	rows, err := tx.QueryContext(ctx, `SELECT rc.contact, rc.agent, rc.role, rc.task, rc.state, rc.at FROM remote_claims rc
JOIN peer_messages pm ON pm.contact = rc.contact AND pm.pairing_epoch = rc.pairing_epoch AND pm.message_id = rc.message_id
WHERE `+sentByAgent+` ORDER BY rc.state = 'released', rc.at, rc.task, rc.role, rc.agent`, messageID, agent)
	if err != nil {
		return set, err
	}
	defer rows.Close()
	for rows.Next() {
		c := protocol.Claim{MessageID: messageID, Remote: &protocol.ClaimRemote{}}
		var at string
		if err := rows.Scan(&c.Remote.Workspace, &c.AgentID, &c.Role, &c.Task, &c.State, &at); err != nil {
			return set, err
		}
		if c.ClaimedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return set, err
		}
		set.Total++
		if len(set.Claims) < protocol.ClaimsPerMessage {
			set.Claims = append(set.Claims, c)
		}
	}
	set.Truncated = set.Total > len(set.Claims)
	return set, rows.Err()
}

// PendingClaimNotices is up to max claim states still owed to contact in
// its current pairing, oldest change first.
func (p *PeerMessages) PendingClaimNotices(ctx context.Context, contact string, max int) ([]messaging.ClaimNotice, error) {
	var out []messaging.ClaimNotice
	err := p.inTx(ctx, contact, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT remote_id, agent, role, task, state, seq, at FROM claim_notices_out
WHERE contact = ? AND pairing_epoch = `+currentEpoch+` AND pending = 1 ORDER BY at, rowid LIMIT ?`,
			contact, contact, sessionBudget.clamp(budget{max, 0}).count)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n messaging.ClaimNotice
			var at string
			if err := rows.Scan(&n.MessageID, &n.Agent, &n.Role, &n.Task, &n.State, &n.Seq, &at); err != nil {
				return err
			}
			if n.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}

// ClaimNoticeAnswered settles the state the contact answered: kept there,
// or dropped for good with its reason. A claim that changed since stays
// pending with its newer state.
func (p *PeerMessages) ClaimNoticeAnswered(ctx context.Context, contact string, r messaging.ClaimReply) error {
	return p.exec(ctx, contact, `UPDATE claim_notices_out SET pending = 0, dropped = ?7
WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND remote_id = ?2 AND agent = ?3 AND role = ?4 AND task = ?5 AND seq = ?6 AND pending = 1`,
		contact, r.MessageID, r.Agent, r.Role, r.Task, r.Seq, r.Reason)
}

// settleUnshownClaims, when a session with contact agreed a version that
// carries no claim notices, settles everything still owed it as unshown
// for that reason: nothing stays queued for a peer that can't take it.
func settleUnshownClaims(ctx context.Context, tx *sql.Tx, contact string, version byte) error {
	if version >= messaging.ClaimVersion {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE claim_notices_out SET pending = 0, dropped = ?2
WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND pending = 1`, contact, protocol.ClaimUnsupported)
	return err
}

// AcceptClaimNotice takes the contact's word on a claim of a message this
// side sent it in the current pairing, by one of that message's
// recipients. Anything else is dropped for good, with the reason, and
// nothing is stored. A state no newer than the one held changes nothing.
func (p *PeerMessages) AcceptClaimNotice(ctx context.Context, contact string, n messaging.ClaimNotice, at time.Time) (messaging.ClaimAnswer, error) {
	var answer messaging.ClaimAnswer
	err := p.inTx(ctx, contact, func(tx *sql.Tx) error {
		epoch, err := contactEpoch(ctx, tx, contact)
		if err != nil {
			return err
		}
		k := pairingKey{contact, epoch}
		if answer.Reason, err = unclaimable(ctx, tx, k, n); err != nil || answer.Reason != "" {
			return err
		}
		answer.Reason, err = holdClaim(ctx, tx, k, n, at)
		answer.Stored = err == nil && answer.Reason == ""
		return err
	})
	return answer, err
}

// unclaimable is why the notice names nothing its agent could claim: no
// message this side sent that contact in this pairing, or one that wasn't
// to that agent, as its recipients were frozen when it was sent.
func unclaimable(ctx context.Context, tx *sql.Tx, k pairingKey, n messaging.ClaimNotice) (string, error) {
	var to sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT to_agents FROM peer_messages WHERE contact = ? AND pairing_epoch = ? AND direction = 'out' AND message_id = ?`,
		k.contact, k.epoch, n.MessageID).Scan(&to)
	if errors.Is(err, sql.ErrNoRows) {
		return messaging.ClaimUnknownMessage, nil
	}
	if err != nil {
		return "", err
	}
	recipients, err := groupList(to)
	if err != nil || slices.Contains(recipients, n.Agent) {
		return "", err
	}
	return messaging.ClaimNotRecipient, nil
}

// holdClaim keeps the notice's state unless one at least as new is held:
// the same state again is kept as it is, an older one is stale, and the
// same sequence number with another content is a conflict.
func holdClaim(ctx context.Context, tx *sql.Tx, k pairingKey, n messaging.ClaimNotice, at time.Time) (string, error) {
	var seq uint64
	var state, held string
	err := tx.QueryRowContext(ctx, `SELECT seq, state, at FROM remote_claims WHERE contact = ? AND pairing_epoch = ? AND message_id = ? AND agent = ? AND role = ? AND task = ?`,
		k.contact, k.epoch, n.MessageID, n.Agent, n.Role, n.Task).Scan(&seq, &state, &held)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return "", err
	case n.Seq < seq:
		return messaging.ClaimStale, nil
	case n.Seq == seq:
		return sameClaim(n, state, held), nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO remote_claims (contact, pairing_epoch, message_id, agent, role, task, state, seq, at, received_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (contact, pairing_epoch, message_id, agent, role, task)
DO UPDATE SET state = excluded.state, seq = excluded.seq, at = excluded.at, received_at = excluded.received_at`,
		k.contact, k.epoch, n.MessageID, n.Agent, n.Role, n.Task, n.State, n.Seq, stamp(n.At), stamp(at))
	return "", err
}

// sameClaim: the notice repeats the state held under its sequence number.
func sameClaim(n messaging.ClaimNotice, state, at string) string {
	if n.State == state && stamp(n.At) == at {
		return ""
	}
	return messaging.ClaimConflict
}
