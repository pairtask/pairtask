package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Review requests from a paired workspace (#235). This side's operator
// grants, per contact and agent, whether that agent may be asked: each
// request waits for the operator (ask), or goes to the agent at once
// (auto); and whether a verdict may be published, and formally. A request
// is staged like a message for agents, with its pins and policy frozen,
// and becomes a review only when the hub dispatches it under the grant
// that holds then.

// peerReviewGrantsColumns keeps the review grants.
// The transition creates it, or rebuilds a released one, with
// pairing_epoch in its key (#276).
const peerReviewGrantsColumns = `(
    contact    TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    agent      TEXT NOT NULL,
    mode       TEXT NOT NULL CHECK (mode IN ('ask', 'auto')),
    publish    INTEGER NOT NULL,
    formal     INTEGER NOT NULL,
    granted_at TEXT NOT NULL,
    PRIMARY KEY (contact, pairing_epoch, agent)
)`

// Review grant modes.
const (
	ReviewAsk  = "ask"  // each request waits for this side's operator
	ReviewAuto = "auto" // each request goes to the agent at once
	ReviewOff  = "off"  // as a change: no requests at all
)

// Import kinds and the states only review requests take.
const (
	importMessage  = "message"
	importReview   = "review"
	importOutcome  = "outcome"  // the notice of a review request's outcome, for the agent that sent it
	ImportAwaiting = "awaiting" // a review request waiting for this side's operator
)

// Bounds on review requests not yet dispatched or decided.
const (
	ReviewAsksPerAgent   = 16
	ReviewAsksPerContact = 64
	ReviewAsksTotal      = 256
	// ReviewAskExpiry is how long a request waits for the operator.
	ReviewAskExpiry = 7 * 24 * time.Hour
)

var (
	// ErrReviewGrant is a grant change outside the rules.
	ErrReviewGrant = fmt.Errorf("%w: review grant", messaging.ErrInvalid)
	// ErrNotAwaiting is a decision on a request that isn't waiting for one.
	ErrNotAwaiting = errors.New("that review request isn't waiting for a decision")
)

// ReviewGrant is what an agent may be asked by a contact.
type ReviewGrant struct {
	Agent     string    `json:"agent"`
	Mode      string    `json:"mode"`
	Publish   bool      `json:"publish"`
	Formal    bool      `json:"formal"`
	GrantedAt time.Time `json:"granted_at"`
}

// ReviewGrantChange sets the grant of named agents: a mode, or off.
// Formal needs publish.
type ReviewGrantChange struct {
	Agents          []string
	Mode            string
	Publish, Formal bool
	Now             time.Time
}

// grantModes are the modes a change may set.
var grantModes = map[string]bool{ReviewAsk: true, ReviewAuto: true, ReviewOff: true}

func (g ReviewGrantChange) valid() bool {
	if !grantModes[g.Mode] || len(g.Agents) == 0 {
		return false
	}
	if g.Formal && !g.Publish {
		return false
	}
	return !slices.ContainsFunc(g.Agents, func(a string) bool { return !messaging.ValidAgent(a) })
}

// SetReviewGrant changes what a confirmed contact may ask of agents, and
// answers its grants. Off withholds that contact's requests for them not
// yet dispatched; reviews already made stay as they are.
func (p *PeerMessages) SetReviewGrant(ctx context.Context, contact string, g ReviewGrantChange) ([]ReviewGrant, error) {
	if !g.valid() {
		return nil, ErrReviewGrant
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := requireContact(ctx, tx, contactKey(contact)); err != nil {
		return nil, err
	}
	for _, agent := range g.Agents {
		if err := setGrant(ctx, tx, grantRow{contact, agent, g}); err != nil {
			return nil, err
		}
	}
	grants, err := reviewGrants(ctx, tx, contact)
	if err != nil {
		return nil, err
	}
	return grants, tx.Commit()
}

type grantRow struct {
	contact, agent string
	change         ReviewGrantChange
}

func setGrant(ctx context.Context, tx *sql.Tx, r grantRow) error {
	g := r.change
	if g.Mode == ReviewOff {
		if _, err := tx.ExecContext(ctx, `DELETE FROM peer_review_grants WHERE contact = ? AND pairing_epoch = `+currentEpoch+` AND agent = ?`,
			r.contact, r.contact, r.agent); err != nil {
			return err
		}
		// Off is an explicit decision: it never reads as undecided (#293).
		reviewOps := []string{protocol.ConsentReview, protocol.ConsentPublish}
		if err := denyScopes(ctx, tx, denial{r.contact, []string{r.agent}, reviewOps, g.Now}); err != nil {
			return err
		}
		return withholdAsks(ctx, tx, askScope{r.contact, r.agent, "unshared", g.Now})
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO peer_review_grants (contact, pairing_epoch, agent, mode, publish, formal, granted_at)
VALUES (?, `+currentEpoch+`, ?, ?, ?, ?, ?)
ON CONFLICT (contact, pairing_epoch, agent) DO UPDATE SET mode = excluded.mode, publish = excluded.publish, formal = excluded.formal, granted_at = excluded.granted_at`,
		r.contact, r.contact, r.agent, g.Mode, g.Publish, g.Formal, msgStamp(g.Now))
	return err
}

// askScope is a contact's review requests for one agent, or for all ("").
type askScope struct {
	contact, agent, reason string
	at                     time.Time
}

// withholdAsks settles, as withheld, review requests of the contact's
// current pairing not yet dispatched.
func withholdAsks(ctx context.Context, tx *sql.Tx, s askScope) error {
	ids, err := queryStrings(ctx, tx, `UPDATE remote_imports SET state = 'withheld', reason = ?1, settled_at = ?2
WHERE contact = ?3 AND pairing_epoch = `+currentEpochAt(3)+` AND kind = 'review' AND state IN ('pending', 'awaiting')
    AND (?4 = '' OR recipients = json_array(?4)) RETURNING remote_id`,
		s.reason, msgStamp(s.at), s.contact, s.agent)
	if err != nil {
		return err
	}
	if err := settleConsentOutcomes(ctx, tx, s.contact); err != nil { // an allowed hold withheld is told so (#293)
		return err
	}
	return askOutcomes(ctx, tx, askScope{s.contact, "", "withheld", s.at}, ids)
}

// askOutcomes tells the requester of each request, of the contact's
// current pairing, the scope's outcome.
func askOutcomes(ctx context.Context, tx *sql.Tx, s askScope, remoteIDs []string) error {
	src, err := currentPairing(ctx, tx, s.contact)
	if err != nil {
		return err
	}
	reason := map[string]string{"withheld": "unshared"}[s.reason]
	for _, id := range remoteIDs {
		imp := RemoteImport{Contact: src.contact, Epoch: src.epoch, RemoteID: id}
		if err := askOutcome(ctx, tx, imp, askEnd{s.reason, reason, s.at}); err != nil {
			return err
		}
	}
	return nil
}

// ReviewGrants are a contact's grants in its current pairing, by agent.
func (p *PeerMessages) ReviewGrants(ctx context.Context, contact string) ([]ReviewGrant, error) {
	return reviewGrants(ctx, p.db, contact)
}

func reviewGrants(ctx context.Context, q rowsQuerier, contact string) ([]ReviewGrant, error) {
	rows, err := q.QueryContext(ctx, `SELECT agent, mode, publish, formal, granted_at FROM peer_review_grants
WHERE contact = ? AND pairing_epoch = `+currentEpoch+` ORDER BY agent`, contact, contact)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewGrant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func scanGrant(row rowScanner) (ReviewGrant, error) {
	var g ReviewGrant
	var at string
	if err := row.Scan(&g.Agent, &g.Mode, &g.Publish, &g.Formal, &at); err != nil {
		return g, err
	}
	var err error
	g.GrantedAt, err = time.Parse(time.RFC3339Nano, at)
	return g, err
}

// reviewGrant is the grant a contact has for agent now, in its current
// pairing; found is false for none.
func reviewGrant(ctx context.Context, q rowQuerier, contact, agent string) (ReviewGrant, bool, error) {
	g, err := scanGrant(q.QueryRowContext(ctx, `SELECT agent, mode, publish, formal, granted_at FROM peer_review_grants
WHERE contact = ? AND pairing_epoch = `+currentEpoch+` AND agent = ?`, contact, contact, agent))
	if errors.Is(err, sql.ErrNoRows) {
		return ReviewGrant{}, false, nil
	}
	return g, err == nil, err
}

// askState is where a newly staged request starts: waiting for the
// operator, or for dispatch.
func askState(g ReviewGrant) string {
	if g.Mode == ReviewAuto {
		return ImportPending
	}
	return ImportAwaiting
}

// asksFull: the requests not yet dispatched or decided, held ones
// included (#293), are at a bound: for this agent from this contact, from
// this contact, or in all; or one more of this request's size would pass
// the shared byte bounds, for the contact or in all.
func asksFull(ctx context.Context, tx *sql.Tx, contact string, m messaging.Message) (bool, error) {
	full, err := askCountsFull(ctx, tx, contact, m.To[0])
	if full || err != nil {
		return full, err
	}
	use, err := readHeldUse(ctx, tx, contact)
	return !use.bytesWithin(heldSize(m)), err
}

// askCountsFull: the requests not yet dispatched or decided are at a count
// bound.
func askCountsFull(ctx context.Context, tx *sql.Tx, contact, agent string) (bool, error) {
	var mine, theirs, total int
	err := tx.QueryRowContext(ctx, heldNow+`SELECT
    COALESCE(SUM(contact = ?1 AND EXISTS (SELECT 1 FROM json_each(held.recipients) j WHERE j.value = ?2)), 0),
    COALESCE(SUM(contact = ?1), 0), COUNT(*) FROM held`, contact, agent).Scan(&mine, &theirs, &total)
	if err != nil {
		return false, err
	}
	return mine >= ReviewAsksPerAgent || theirs >= ReviewAsksPerContact || total >= ReviewAsksTotal, nil
}

// AwaitingReviewCounts are, by contact, the review requests of its current
// pairing waiting for this side's operator (#276); a contact with none is
// absent.
func (s *PeerMessages) AwaitingReviewCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT contact, COUNT(*) FROM remote_imports
WHERE kind = 'review' AND state = 'awaiting' AND pairing_epoch = `+currentEpochFor("remote_imports.contact")+` GROUP BY contact`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var contact string
		var n int
		if err := rows.Scan(&contact, &n); err != nil {
			return nil, err
		}
		counts[contact] = n
	}
	return counts, rows.Err()
}

// DecideRemoteReview is this side's operator's decision on a request that
// waits for one: approved, it goes to dispatch; declined, it is withheld.
// A request past ReviewAskExpiry is withheld as expired instead. Anything
// else answers its state unchanged with ErrNotAwaiting, so a decision is
// made once and nothing settled comes back.
// RemoteDecision is this side's operator's decision on a contact's review
// request.
type RemoteDecision struct {
	Contact, RemoteID string
	Approve           bool
	Now               time.Time
}

func (s *Store) DecideRemoteReview(ctx context.Context, d RemoteDecision) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	a, err := stagedReview(ctx, tx, d.Contact, d.RemoteID)
	if err != nil {
		return "", err
	}
	if a.state != ImportAwaiting {
		return a.state, ErrNotAwaiting
	}
	next, reason := decision(d.Approve, a.staged, d.Now)
	if err := decideAsk(ctx, tx, a.imp, askEnd{next, reason, d.Now}); err != nil {
		return "", err
	}
	return next, tx.Commit()
}

// decideAsk moves a waiting request where the decision sends it, and
// tells its requester one withheld.
func decideAsk(ctx context.Context, tx *sql.Tx, imp RemoteImport, e askEnd) error {
	src := imp.source()
	if _, err := tx.ExecContext(ctx, `UPDATE remote_imports SET state = ?1, reason = ?2, approved_at = CASE WHEN ?1 = 'pending' THEN ?3 END,
    settled_at = CASE WHEN ?1 = 'withheld' THEN ?3 END WHERE contact = ?4 AND pairing_epoch = ?5 AND remote_id = ?6`,
		e.state, e.reason, msgStamp(e.at), src.contact, src.epoch, imp.RemoteID); err != nil {
		return err
	}
	if e.state != ImportWithheld {
		return nil
	}
	return askOutcome(ctx, tx, imp, askEnd{e.reason, "", e.at})
}

// stagedAskRow is a staged review request of a contact's current pairing:
// its import's key, its state and when it was staged.
type stagedAskRow struct {
	imp           RemoteImport
	state, staged string
}

// stagedReview is a contact's staged review request in its current
// pairing. A paused contact's requests wait for a decision until it
// resumes (#276).
func stagedReview(ctx context.Context, tx *sql.Tx, contact, remoteID string) (stagedAskRow, error) {
	if err := requireActive(ctx, tx, contact); err != nil {
		return stagedAskRow{}, err
	}
	src, err := currentPairing(ctx, tx, contact)
	if err != nil {
		return stagedAskRow{}, err
	}
	a := stagedAskRow{imp: RemoteImport{Contact: src.contact, Epoch: src.epoch, RemoteID: remoteID}}
	err = tx.QueryRowContext(ctx, `SELECT state, staged_at FROM remote_imports WHERE contact = ? AND pairing_epoch = ? AND remote_id = ? AND kind = 'review'`,
		src.contact, src.epoch, remoteID).Scan(&a.state, &a.staged)
	if errors.Is(err, sql.ErrNoRows) {
		return stagedAskRow{}, ErrNoImport
	}
	return a, err
}

// decision is a waiting request's next state and why: expired before any
// decision counts, then the operator's.
func decision(approve bool, staged string, now time.Time) (string, string) {
	if staged < msgStamp(now.Add(-ReviewAskExpiry)) {
		return ImportWithheld, "expired"
	}
	if approve {
		return ImportPending, ""
	}
	return ImportWithheld, "declined"
}

// expireAsks withholds a contact's requests of its current pairing that
// waited past ReviewAskExpiry.
func expireAsks(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	ids, err := queryStrings(ctx, tx, `UPDATE remote_imports SET state = 'withheld', reason = 'expired', settled_at = ?1
WHERE contact = ?2 AND pairing_epoch = `+currentEpochAt(2)+` AND kind = 'review' AND state = 'awaiting' AND staged_at < ?3 RETURNING remote_id`,
		msgStamp(now), contact, msgStamp(now.Add(-ReviewAskExpiry)))
	if err != nil {
		return err
	}
	return askOutcomes(ctx, tx, askScope{contact, "", "expired", now}, ids)
}

// reviewAskJSON stores a request's ask with its row, NULL for a message.
func reviewAskJSON(r *messaging.ReviewAsk) sql.NullString {
	if r == nil {
		return sql.NullString{}
	}
	data, _ := json.Marshal(r) // a struct of strings and bools always encodes
	return sql.NullString{String: string(data), Valid: true}
}

func scanReviewAsk(data sql.NullString) (*messaging.ReviewAsk, error) {
	if !data.Valid {
		return nil, nil
	}
	var r messaging.ReviewAsk
	if err := json.Unmarshal([]byte(data.String), &r); err != nil {
		return nil, fmt.Errorf("review ask: %w", err)
	}
	return &r, nil
}

// AskStatus is where a review request a contact sent here is.
type AskStatus struct {
	State, ReviewID, Reason string
}

// AskStatus is a request's state in the contact's current pairing, the
// review it became once imported, and why it was withheld; found is false
// for a message that isn't one.
func (p *PeerMessages) AskStatus(ctx context.Context, contact, remoteID string) (AskStatus, bool, error) {
	var a AskStatus
	err := p.db.QueryRowContext(ctx, `SELECT state, CASE WHEN state = 'imported' THEN local_id ELSE '' END, COALESCE(reason, '')
FROM remote_imports WHERE contact = ?1 AND pairing_epoch = `+currentEpochOf1+` AND remote_id = ?2 AND kind = 'review'`,
		contact, remoteID).Scan(&a.State, &a.ReviewID, &a.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	return a, err == nil, err
}

// IsReview: the import is a review request, whose outcomes go back.
func (imp RemoteImport) IsReview() bool { return imp.Kind == importReview }
