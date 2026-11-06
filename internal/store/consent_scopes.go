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

// A scope is one contact, local agent and operation. It is allowed by the
// grant that holds now, denied by an explicit decision, or undecided; with
// no decision row a legacy contact's scope is denied, a newer one's
// undecided.

// Scope states.
const (
	scopeAllowed   = "allowed"
	scopeUndecided = "undecided"
	scopeDenied    = "denied"
	scopeSession   = "session" // a live session grant covers it; never stored
)

// consentOperations are the operations a scope may name.
var consentOperations = map[string]bool{protocol.ConsentMessage: true, protocol.ConsentReview: true, protocol.ConsentPublish: true}

// consentScope is one contact's agent and operation.
type consentScope struct{ contact, agent, operation string }

// requestTargets is what a request asks of each recipient, in canonical
// order: agents sorted once each; a message asks message, a review request
// review, and publish when its policy asks to publish.
func requestTargets(m messaging.Message) []protocol.ConsentTarget {
	ops := []string{protocol.ConsentMessage}
	if m.Review != nil {
		ops = []string{protocol.ConsentReview}
		if m.Review.Publish {
			ops = append(ops, protocol.ConsentPublish)
		}
	}
	agents := slices.Clone(m.To)
	slices.Sort(agents)
	var out []protocol.ConsentTarget
	for _, agent := range slices.Compact(agents) {
		out = append(out, protocol.ConsentTarget{Agent: agent, Operations: slices.Clone(ops)})
	}
	return out
}

// targetScopes are a request's scopes for a contact.
func targetScopes(contact string, targets []protocol.ConsentTarget) []consentScope {
	var out []consentScope
	for _, t := range targets {
		for _, op := range t.Operations {
			out = append(out, consentScope{contact, t.Agent, op})
		}
	}
	return out
}

// scopeQuery is what a scope's state reads besides the scope: the review
// request it is for, and the live owner whose session grants count; none
// for a decision, which never reads a session grant as decided.
type scopeQuery struct {
	ask   *messaging.ReviewAsk
	owner LiveOwner
}

// scopeStates are the state of each scope now, in order.
func scopeStates(ctx context.Context, tx *sql.Tx, scopes []consentScope, q scopeQuery) ([]string, error) {
	legacy, err := scopesLegacy(ctx, tx, scopes)
	if err != nil {
		return nil, err
	}
	states := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		state, err := scopeState(ctx, tx, sc, scopeContext{q, legacy})
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}

// scopesLegacy: the scopes' contact was paired before consent existed.
func scopesLegacy(ctx context.Context, tx *sql.Tx, scopes []consentScope) (bool, error) {
	if len(scopes) == 0 {
		return false, nil
	}
	var legacy bool
	err := tx.QueryRowContext(ctx, `SELECT consent_legacy FROM peer_contacts WHERE identity_key = ?`, scopes[0].contact).Scan(&legacy)
	return legacy, err
}

// scopeContext is a scope's query, and whether its contact is legacy.
type scopeContext struct {
	scopeQuery
	legacy bool
}

// scopeState is a scope's state: the grant first, then an explicit
// denial, then a live session grant, then a decision or the contact's
// default. A session never outranks a denial.
func scopeState(ctx context.Context, tx *sql.Tx, sc consentScope, c scopeContext) (string, error) {
	allowed, err := scopeGranted(ctx, tx, sc, c.ask)
	if err != nil || allowed {
		return scopeAllowed, err
	}
	decided, err := decidedState(ctx, tx, sc, c.legacy)
	if err != nil || decided == scopeDenied {
		return decided, err
	}
	session, err := sessionCovers(ctx, tx, sc, c.scopeQuery)
	if err != nil || !session {
		return decided, err
	}
	return scopeSession, nil
}

// decidedState is a scope's explicit decision, or with none the contact's
// default: denied for a legacy contact, else undecided.
func decidedState(ctx context.Context, tx *sql.Tx, sc consentScope, legacy bool) (string, error) {
	state, found, err := explicitState(ctx, tx, sc)
	switch {
	case err != nil || found:
		return state, err
	case legacy:
		return scopeDenied, nil
	}
	return scopeUndecided, nil
}

// explicitState is a scope's explicit decision row, if any.
func explicitState(ctx context.Context, tx *sql.Tx, sc consentScope) (string, bool, error) {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM consent_decisions WHERE contact = ? AND agent = ? AND operation = ?`,
		sc.contact, sc.agent, sc.operation).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return state, err == nil, err
}

// explicitlyDenied: the scope has no grant now and the operator denied it
// explicitly; a legacy default is not a denial here.
func explicitlyDenied(ctx context.Context, tx *sql.Tx, sc consentScope, ask *messaging.ReviewAsk) (bool, error) {
	allowed, err := scopeGranted(ctx, tx, sc, ask)
	if err != nil || allowed {
		return false, err
	}
	state, _, err := explicitState(ctx, tx, sc)
	return state == scopeDenied, err
}

// scopeGranted: the grant that holds now allows the scope; publication
// within the asked ceiling, formal only when the grant is formal.
func scopeGranted(ctx context.Context, tx *sql.Tx, sc consentScope, ask *messaging.ReviewAsk) (bool, error) {
	if sc.operation == protocol.ConsentMessage {
		return allGranted(ctx, tx, sc.contact, []string{sc.agent})
	}
	g, granted, err := reviewGrant(ctx, tx, sc.contact, sc.agent)
	if err != nil || !granted {
		return false, err
	}
	if sc.operation == protocol.ConsentReview {
		return true, nil
	}
	return g.allows(protocol.ReviewPolicy{Publish: true, Formal: askedFormal(sc, ask)}), nil
}

// holdable: nothing is explicitly denied and something needs a decision.
func holdable(states []string) bool {
	return !slices.Contains(states, scopeDenied) && slices.Contains(states, scopeUndecided)
}

// sessionAdmits: nothing needs a decision, and a live session grant
// covers something no persistent grant does.
func sessionAdmits(states []string) bool {
	return !slices.ContainsFunc(states, func(s string) bool { return s != scopeAllowed && s != scopeSession }) &&
		slices.Contains(states, scopeSession)
}

// scopeDecision is one scope's explicit state, by which audit and when.
type scopeDecision struct {
	scope   consentScope
	state   string
	auditID int64
	at      time.Time
}

// decideScope records a scope's explicit state.
func decideScope(ctx context.Context, tx *sql.Tx, d scopeDecision) error {
	if d.state == scopeDenied {
		if err := endSessions(ctx, tx, d.scope); err != nil { // a denial ends the scope's session too
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO consent_decisions (contact, pairing_epoch, agent, operation, state, decided_at, audit_id)
VALUES (?, `+currentEpoch+`, ?, ?, ?, ?, ?) ON CONFLICT (contact, pairing_epoch, agent, operation) DO UPDATE SET
    state = excluded.state, decided_at = excluded.decided_at, audit_id = excluded.audit_id`,
		d.scope.contact, d.scope.contact, d.scope.agent, d.scope.operation, d.state, msgStamp(d.at), nullAudit(d.auditID))
	return err
}

// denial is a contact's agents whose operations a revoke denies, and when.
type denial struct {
	contact string
	agents  []string
	ops     []string
	at      time.Time
}

// denyScopes records an explicit denial of each agent's operations, as a
// revoke does: absence of a grant then never reads as undecided.
func denyScopes(ctx context.Context, tx *sql.Tx, d denial) error {
	for _, agent := range d.agents {
		for _, op := range d.ops {
			if err := decideScope(ctx, tx, scopeDecision{consentScope{d.contact, agent, op}, scopeDenied, 0, d.at}); err != nil {
				return err
			}
		}
	}
	return nil
}

// nullAudit is an audit ID as stored, NULL for none.
func nullAudit(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id != 0} }
