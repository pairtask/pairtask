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
)

// Review requests from a paired workspace (#235). This side's operator
// grants, per contact and agent, whether that agent may be asked: each
// request waits for the operator (ask), or goes to the agent at once
// (auto); and whether a verdict may be published, and formally. A request
// is staged like a message for agents, with its pins and policy frozen,
// and becomes a review only when the hub dispatches it under the grant
// that holds then.

// peerReviewsSchema keeps the review grants.
const peerReviewsSchema = `
CREATE TABLE IF NOT EXISTS peer_review_grants (
    contact    TEXT NOT NULL,
    agent      TEXT NOT NULL,
    mode       TEXT NOT NULL CHECK (mode IN ('ask', 'auto')),
    publish    INTEGER NOT NULL,
    formal     INTEGER NOT NULL,
    granted_at TEXT NOT NULL,
    PRIMARY KEY (contact, agent)
);
`

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
		if _, err := tx.ExecContext(ctx, `DELETE FROM peer_review_grants WHERE contact = ? AND agent = ?`, r.contact, r.agent); err != nil {
			return err
		}
		return withholdAsks(ctx, tx, askScope{r.contact, r.agent, "unshared", g.Now})
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO peer_review_grants (contact, agent, mode, publish, formal, granted_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (contact, agent) DO UPDATE SET mode = excluded.mode, publish = excluded.publish, formal = excluded.formal, granted_at = excluded.granted_at`,
		r.contact, r.agent, g.Mode, g.Publish, g.Formal, msgStamp(g.Now))
	return err
}

// askScope is a contact's review requests for one agent, or for all ("").
type askScope struct {
	contact, agent, reason string
	at                     time.Time
}

// withholdAsks settles, as withheld, review requests not yet dispatched.
func withholdAsks(ctx context.Context, tx *sql.Tx, s askScope) error {
	ids, err := queryStrings(ctx, tx, `UPDATE remote_imports SET state = 'withheld', reason = ?1, settled_at = ?2
WHERE contact = ?3 AND kind = 'review' AND state IN ('pending', 'awaiting') AND (?4 = '' OR recipients = json_array(?4)) RETURNING remote_id`,
		s.reason, msgStamp(s.at), s.contact, s.agent)
	if err != nil {
		return err
	}
	return askOutcomes(ctx, tx, askScope{s.contact, "", "withheld", s.at}, ids)
}

// askOutcomes tells the requester of each request the scope's outcome.
func askOutcomes(ctx context.Context, tx *sql.Tx, s askScope, remoteIDs []string) error {
	reason := map[string]string{"withheld": "unshared"}[s.reason]
	for _, id := range remoteIDs {
		if err := askOutcome(ctx, tx, RemoteImport{Contact: s.contact, RemoteID: id}, askEnd{s.reason, reason, s.at}); err != nil {
			return err
		}
	}
	return nil
}

// ReviewGrants are a contact's grants, by agent.
func (p *PeerMessages) ReviewGrants(ctx context.Context, contact string) ([]ReviewGrant, error) {
	return reviewGrants(ctx, p.db, contact)
}

func reviewGrants(ctx context.Context, q rowsQuerier, contact string) ([]ReviewGrant, error) {
	rows, err := q.QueryContext(ctx, `SELECT agent, mode, publish, formal, granted_at FROM peer_review_grants WHERE contact = ? ORDER BY agent`, contact)
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

// reviewGrant is the grant a contact has for agent now; found is false for none.
func reviewGrant(ctx context.Context, q rowQuerier, contact, agent string) (ReviewGrant, bool, error) {
	g, err := scanGrant(q.QueryRowContext(ctx, `SELECT agent, mode, publish, formal, granted_at FROM peer_review_grants
WHERE contact = ? AND agent = ?`, contact, agent))
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

// asksFull: the requests not yet dispatched or decided are at a bound: for
// this agent from this contact, from this contact, or in all.
func asksFull(ctx context.Context, tx *sql.Tx, contact, agent string) (bool, error) {
	var mine, theirs, total int
	err := tx.QueryRowContext(ctx, `SELECT
    COALESCE(SUM(contact = ?1 AND recipients = json_array(?2)), 0), COALESCE(SUM(contact = ?1), 0), COUNT(*)
FROM remote_imports WHERE kind = 'review' AND state IN ('pending', 'awaiting')`, contact, agent).Scan(&mine, &theirs, &total)
	if err != nil {
		return false, err
	}
	return mine >= ReviewAsksPerAgent || theirs >= ReviewAsksPerContact || total >= ReviewAsksTotal, nil
}

// AwaitingReviewCounts are, by contact, the review requests waiting for
// this side's operator; a contact with none is absent.
func (s *PeerMessages) AwaitingReviewCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT contact, COUNT(*) FROM remote_imports
WHERE kind = 'review' AND state = 'awaiting' GROUP BY contact`)
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
	contact, remoteID, approve, now := d.Contact, d.RemoteID, d.Approve, d.Now
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// A paused contact's requests wait for a decision until it resumes (#276).
	if err := requireActive(ctx, tx, contact); err != nil {
		return "", err
	}
	var state, staged string
	err = tx.QueryRowContext(ctx, `SELECT state, staged_at FROM remote_imports WHERE contact = ? AND remote_id = ? AND kind = 'review'`,
		contact, remoteID).Scan(&state, &staged)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoImport
	}
	if err != nil {
		return "", err
	}
	if state != ImportAwaiting {
		return state, ErrNotAwaiting
	}
	next, reason := decision(approve, staged, now)
	if _, err := tx.ExecContext(ctx, `UPDATE remote_imports SET state = ?1, reason = ?2, approved_at = CASE WHEN ?1 = 'pending' THEN ?3 END,
    settled_at = CASE WHEN ?1 = 'withheld' THEN ?3 END WHERE contact = ?4 AND remote_id = ?5`,
		next, reason, msgStamp(now), contact, remoteID); err != nil {
		return "", err
	}
	if next == ImportWithheld {
		if err := askOutcome(ctx, tx, RemoteImport{Contact: contact, RemoteID: remoteID}, askEnd{reason, "", now}); err != nil {
			return "", err
		}
	}
	return next, tx.Commit()
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

// expireAsks withholds a contact's requests that waited past ReviewAskExpiry.
func expireAsks(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	ids, err := queryStrings(ctx, tx, `UPDATE remote_imports SET state = 'withheld', reason = 'expired', settled_at = ?1
WHERE contact = ?2 AND kind = 'review' AND state = 'awaiting' AND staged_at < ?3 RETURNING remote_id`,
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

// AskStatus is a request's state, the review it became once imported, and
// why it was withheld; found is false for a message that isn't one.
func (p *PeerMessages) AskStatus(ctx context.Context, contact, remoteID string) (AskStatus, bool, error) {
	var a AskStatus
	err := p.db.QueryRowContext(ctx, `SELECT state, CASE WHEN state = 'imported' THEN local_id ELSE '' END, COALESCE(reason, '')
FROM remote_imports WHERE contact = ? AND remote_id = ? AND kind = 'review'`, contact, remoteID).Scan(&a.State, &a.ReviewID, &a.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	return a, err == nil, err
}

// IsReview: the import is a review request, whose outcomes go back.
func (imp RemoteImport) IsReview() bool { return imp.Kind == importReview }
