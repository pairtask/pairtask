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
	Set      protocol.ClaimSet
	Received bool // this side received the subject: the claims are its own agents'
	Changed  bool // the request changed a claim
	// Known says Visible and Reason describe something: always after a
	// change; on a read only when the caller has a claim on the message.
	Known   bool
	Visible bool
	Reason  string
	Contact string // the workspace the message came from
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
	out, changeErr := claimReceived(ctx, tx, claimChange{kind, req, imp})
	out.Received = true
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

// claimChange is one claim frame about a message received from a paired
// workspace: what it asks, by whom, of which import.
type claimChange struct {
	kind string
	req  ClaimRequest
	imp  RemoteImport
}

// claimReceived applies a claim frame to a received message. A change is
// made locally and, in the same transaction, its state is recorded as owed
// to the sender, or as hidden from it with the reason: both commit or
// neither does. A read reports what the caller's claims there were told.
func claimReceived(ctx context.Context, tx *sql.Tx, c claimChange) (PairedClaim, error) {
	state, err := changeClaim(ctx, tx, c.kind, c.req)
	if err != nil {
		return PairedClaim{}, err
	}
	out := PairedClaim{Changed: state != "", Contact: c.imp.Contact}
	if !out.Changed {
		err = toldOf(ctx, tx, c, &out)
		return out, err
	}
	out.Known = true
	out.Reason, err = hiddenFrom(ctx, tx, c.imp, c.req.Agent)
	if err != nil {
		return out, err
	}
	out.Visible = out.Reason == ""
	return out, recordClaimNotice(ctx, tx, c, claimTold{state, out.Reason})
}

// toldOf fills in, for a read, what the sender is shown of the caller's
// claims on the message: nothing once the pairing the message arrived
// under has ended, whatever it was told before; else shown when any of
// them is; else the one reason they are all hidden for, or mixed when the
// reasons differ. A caller with no claim there is told nothing about
// visibility.
func toldOf(ctx context.Context, tx *sql.Tx, c claimChange, out *PairedClaim) error {
	reasons, err := queryStrings(ctx, tx, `SELECT DISTINCT dropped FROM claim_notices_out
WHERE contact = ? AND pairing_epoch = ? AND remote_id = ? AND agent = ? ORDER BY dropped`,
		c.imp.Contact, c.imp.source().epoch, c.imp.RemoteID, c.req.Agent)
	if err != nil || len(reasons) == 0 {
		return err
	}
	out.Known = true
	current, err := pairingCurrent(ctx, tx, c.imp)
	if err != nil {
		return err
	}
	switch {
	case !current:
		out.Reason = protocol.ClaimUnpaired // whatever it was told, that pairing has ended
	case reasons[0] == "":
		out.Visible = true
	case len(reasons) == 1:
		out.Reason = reasons[0]
	default:
		out.Reason = protocol.ClaimMixed
	}
	return nil
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

// pairingCurrent: the pairing imp arrived under is the contact's current
// one, and it hasn't been unpaired.
func pairingCurrent(ctx context.Context, tx *sql.Tx, imp RemoteImport) (bool, error) {
	var current bool
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT c.unpaired_at = '' AND c.pairing_epoch = ?2 FROM peer_contacts c WHERE c.identity_key = ?1), 0)`,
		imp.Contact, imp.source().epoch).Scan(&current)
	return current, err
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

// claimTold is a claim's new state and why the sender isn't shown it
// ("" when it is).
type claimTold struct{ state, hidden string }

// recordClaimNotice records the claim's new state under its next sequence
// number: owed to the sender, or kept from it with the reason. A hidden
// state is recorded too, never sent, so a read can say why and the next
// state the sender is shown has a sequence number above every earlier one.
func recordClaimNotice(ctx context.Context, tx *sql.Tx, c claimChange, t claimTold) error {
	role := c.req.Role
	if role == "" {
		role = protocol.RoleOwner
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO claim_notices_out (contact, pairing_epoch, remote_id, agent, role, task, state, seq, at, pending, dropped)
VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
ON CONFLICT (contact, pairing_epoch, remote_id, agent, role, task)
DO UPDATE SET state = excluded.state, seq = seq + 1, at = excluded.at, pending = excluded.pending, dropped = excluded.dropped`,
		c.imp.Contact, c.imp.source().epoch, c.imp.RemoteID, c.req.Agent, role, c.req.Task, t.state, stamp(c.req.At), t.hidden == "", t.hidden)
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
// Only the current pairing of a workspace still paired is shown: claims
// told under an earlier pairing, or by a workspace since unpaired, stay in
// the record and out of the answer.
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
JOIN peer_contacts c ON c.identity_key = rc.contact AND c.unpaired_at = '' AND c.pairing_epoch = rc.pairing_epoch
WHERE `+sentByAgent+` ORDER BY rc.state = 'released', rc.at, rc.task, rc.role, rc.agent`, messageID, agent)
	if err != nil {
		return set, err
	}
	defer rows.Close()
	set.ClaimPage, err = remoteClaimPage(rows, messageID)
	return set, err
}

func remoteClaimPage(rows *sql.Rows, messageID string) (protocol.ClaimPage, error) {
	page := protocol.ClaimPage{Claims: []protocol.Claim{}}
	for rows.Next() {
		c := protocol.Claim{MessageID: messageID, Remote: &protocol.ClaimRemote{}}
		var at string
		if err := rows.Scan(&c.Remote.Workspace, &c.AgentID, &c.Role, &c.Task, &c.State, &at); err != nil {
			return page, err
		}
		claimedAt, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return page, err
		}
		c.ClaimedAt = claimedAt
		page.Total++
		if len(page.Claims) < protocol.ClaimsPerMessage {
			page.Claims = append(page.Claims, c)
		}
	}
	page.Truncated = page.Total > len(page.Claims)
	return page, rows.Err()
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

// settleUnsharedClaims cancels pending notices from revoked agents in the
// same transaction as Unshare. A later share must not revive those notices.
func settleUnsharedClaims(ctx context.Context, tx *sql.Tx, contact string) error {
	_, err := tx.ExecContext(ctx, `UPDATE claim_notices_out SET pending = 0, dropped = ?2
WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND pending = 1
AND agent NOT IN (SELECT agent FROM peer_grants WHERE contact = ?1 AND pairing_epoch = claim_notices_out.pairing_epoch)`,
		contact, protocol.ClaimNotShared)
	return err
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
		answer.Reason, err = holdClaim(ctx, tx, toldClaim{k, n, at})
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

// toldClaim is a notice taken under a pairing, and when.
type toldClaim struct {
	k  pairingKey
	n  messaging.ClaimNotice
	at time.Time
}

// Bounds on what a paired workspace can make this side keep for one
// message: as many held claims as a message has locally, and a lifetime
// number of claim keys. A released key is kept, never evicted: its
// sequence number is what stops a delayed older notice from bringing the
// claim back.
const remoteClaimKeys = 4 * protocol.ClaimsPerMessage

// holdClaim keeps the notice's state unless one at least as new is held:
// the same state again is kept as it is, an older one is stale, and the
// same sequence number with another content is a conflict. A state that
// would take the message past its bounds is refused as full.
func holdClaim(ctx context.Context, tx *sql.Tx, t toldClaim) (string, error) {
	row, err := heldClaim(ctx, tx, t)
	if err != nil {
		return "", err
	}
	if reason, settled := row.settles(t.n); settled {
		return reason, nil
	}
	full, err := remoteClaimsFull(ctx, tx, t, heldBefore{row.known, row.known && row.state != messaging.ClaimReleased})
	if err != nil || full {
		return fullReason(full), err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO remote_claims (contact, pairing_epoch, message_id, agent, role, task, state, seq, at, received_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (contact, pairing_epoch, message_id, agent, role, task)
DO UPDATE SET state = excluded.state, seq = excluded.seq, at = excluded.at, received_at = excluded.received_at`,
		t.k.contact, t.k.epoch, t.n.MessageID, t.n.Agent, t.n.Role, t.n.Task, t.n.State, t.n.Seq, stamp(t.n.At), stamp(t.at))
	return "", err
}

// keptClaim is what this side keeps for a notice's key, if anything.
type keptClaim struct {
	known     bool
	seq       uint64
	state, at string
}

func heldClaim(ctx context.Context, tx *sql.Tx, t toldClaim) (keptClaim, error) {
	row := keptClaim{known: true}
	err := tx.QueryRowContext(ctx, `SELECT seq, state, at FROM remote_claims WHERE contact = ? AND pairing_epoch = ? AND message_id = ? AND agent = ? AND role = ? AND task = ?`,
		t.k.contact, t.k.epoch, t.n.MessageID, t.n.Agent, t.n.Role, t.n.Task).Scan(&row.seq, &row.state, &row.at)
	if errors.Is(err, sql.ErrNoRows) {
		return keptClaim{}, nil
	}
	return row, err
}

// settles says the notice changes nothing, and how it is answered: an
// older state is stale; the held sequence number again is kept as it is
// when it repeats the held state, else a conflict.
func (r keptClaim) settles(n messaging.ClaimNotice) (string, bool) {
	if !r.known || n.Seq > r.seq {
		return "", false
	}
	if n.Seq < r.seq {
		return messaging.ClaimStale, true
	}
	return sameClaim(n, r.state, r.at), true
}

func fullReason(full bool) string {
	if full {
		return messaging.ClaimFull
	}
	return ""
}

// heldBefore says what this side already keeps for the notice's key: any
// row, and whether that row counts as held (not released).
type heldBefore struct{ known, held bool }

// remoteClaimsFull: taking the notice would put the message past a bound.
// A new key needs room among the lifetime keys; a state that newly holds
// (a new key, or a released one claimed again) needs room among the held.
func remoteClaimsFull(ctx context.Context, tx *sql.Tx, t toldClaim, before heldBefore) (bool, error) {
	var keys, held int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(state != 'released'), 0) FROM remote_claims WHERE contact = ? AND pairing_epoch = ? AND message_id = ?`,
		t.k.contact, t.k.epoch, t.n.MessageID).Scan(&keys, &held)
	if err != nil {
		return false, err
	}
	if !before.known && keys >= remoteClaimKeys {
		return true, nil
	}
	holds := t.n.State != messaging.ClaimReleased && !before.held
	return holds && held >= protocol.ClaimsPerMessage, nil
}

// sameClaim: the notice repeats the state held under its sequence number.
func sameClaim(n messaging.ClaimNotice, state, at string) string {
	if n.State == state && stamp(n.At) == at {
		return ""
	}
	return messaging.ClaimConflict
}
