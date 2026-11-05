package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// A decision is one compare-and-set on a pending request's ID, digest and
// revision, in one transaction with its audit, its evidence, any grant it
// installs and the dispatch it stages; anything failing leaves all as it
// was. The same decision again answers what it did and changes nothing.

// consentChoices are the choices a decision may make, and the state each
// leaves the request in.
var consentChoices = map[string]string{
	protocol.ConsentOnce: consentAllowed, protocol.ConsentSession: consentAllowed, protocol.ConsentAlways: consentAllowed,
	protocol.ConsentDecline: consentDeclined, protocol.ConsentLater: consentLater,
}

// DecideConsent commits one decision atomically, or ErrConsentStale. A
// request found past its lifetime is settled expired, and the decision is
// stale.
func (s *Store) DecideConsent(ctx context.Context, d ConsentDecision) (ConsentOutcome, error) {
	if consentChoices[d.Choice] == "" {
		return ConsentOutcome{}, fmt.Errorf("%w: consent choice", messaging.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ConsentOutcome{}, err
	}
	defer tx.Rollback()
	out, err := decideIn(ctx, tx, d)
	if err != nil {
		return ConsentOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return ConsentOutcome{}, err
	}
	if out.State == consentExpired {
		return out, ErrConsentStale
	}
	return out, nil
}

// decideIn is DecideConsent inside its transaction.
func decideIn(ctx context.Context, tx *sql.Tx, d ConsentDecision) (ConsentOutcome, error) {
	held, found, err := consentByID(ctx, tx, d.ID)
	switch {
	case err != nil:
		return ConsentOutcome{}, err
	case found && held.decidedBy(d):
		return held.outcome(), nil
	case !found || !held.current(d):
		return ConsentOutcome{}, ErrConsentStale
	}
	if err := requireContact(ctx, tx, contactKey(held.Contact)); err != nil {
		return ConsentOutcome{}, err
	}
	if err := requireActive(ctx, tx, held.Contact); err != nil {
		return ConsentOutcome{}, err
	}
	if held.ExpiresAt <= msgStamp(d.Now) {
		return expireHeld(ctx, tx, held, d)
	}
	out, err := applyChoice(ctx, tx, decided{held, d, nil})
	if err != nil {
		return ConsentOutcome{}, err
	}
	return out, finishConsentAudit(ctx, tx, d.AuditID, "consent "+d.Choice)
}

// current: the decision names the request as it still is, pending.
func (c consentRow) current(d ConsentDecision) bool {
	if c.State != consentPending || c.Digest != d.Digest {
		return false
	}
	return c.Revision == d.Revision
}

// decidedBy: this very decision settled the request already.
func (c consentRow) decidedBy(d ConsentDecision) bool {
	if c.State == consentPending || c.Choice != d.Choice {
		return false
	}
	if c.AuditID != d.AuditID || c.Digest != d.Digest {
		return false
	}
	return c.Revision == d.Revision+1
}

// outcome is what a settled request's decision did.
func (c consentRow) outcome() ConsentOutcome {
	return ConsentOutcome{ID: c.ID, State: c.State, Revision: c.Revision, Dispatched: c.State == consentAllowed}
}

// expireHeld settles a request a decision found past its lifetime.
func expireHeld(ctx context.Context, tx *sql.Tx, held consentRow, d ConsentDecision) (ConsentOutcome, error) {
	_, err := tx.ExecContext(ctx, expireConsentQuery+` AND id = ?2`, msgStamp(d.Now), held.ID)
	return ConsentOutcome{ID: held.ID, State: consentExpired, Reason: protocol.ConsentExpired, Revision: held.Revision + 1}, err
}

// decided is a pending request, the decision on it, and the scopes it
// shows as undecided now.
type decided struct {
	held      consentRow
	d         ConsentDecision
	undecided []consentScope
}

// applyChoice makes the decision's change. A scope explicitly denied since
// the request was shown makes it stale.
func applyChoice(ctx context.Context, tx *sql.Tx, c decided) (ConsentOutcome, error) {
	if c.d.Choice == protocol.ConsentLater {
		return ConsentOutcome{ID: c.held.ID, State: consentLater, Revision: c.held.Revision}, nil
	}
	scopes := targetScopes(c.held.Contact, c.held.Targets)
	states, err := scopeStates(ctx, tx, scopes, c.held.Review)
	if err != nil {
		return ConsentOutcome{}, err
	}
	if slices.Contains(states, scopeDenied) {
		return ConsentOutcome{}, ErrConsentStale
	}
	for i, sc := range scopes {
		if states[i] == scopeUndecided {
			c.undecided = append(c.undecided, sc)
		}
	}
	if c.d.Choice == protocol.ConsentDecline {
		return declineHeld(ctx, tx, c)
	}
	return allowHeld(ctx, tx, c)
}

// declineHeld denies each scope the request showed undecided, and settles
// it declined.
func declineHeld(ctx context.Context, tx *sql.Tx, c decided) (ConsentOutcome, error) {
	for _, sc := range c.undecided {
		if err := decideScope(ctx, tx, scopeDecision{sc, scopeDenied, c.d.AuditID, c.d.Now}); err != nil {
			return ConsentOutcome{}, err
		}
	}
	return settleHeld(ctx, tx, c, consentDeclined)
}

// allowHeld settles the request allowed, installing the grant always
// asks for, and stages its delivery or review as an arrival under a grant
// would have been.
func allowHeld(ctx context.Context, tx *sql.Tx, c decided) (ConsentOutcome, error) {
	if c.d.Choice == protocol.ConsentAlways {
		if err := installGrants(ctx, tx, c); err != nil {
			return ConsentOutcome{}, err
		}
	}
	out, err := settleHeld(ctx, tx, c, consentAllowed)
	if err != nil {
		return ConsentOutcome{}, err
	}
	m, err := c.held.message()
	if err != nil {
		return ConsentOutcome{}, err
	}
	r := newRow{key: messageKey{c.held.Contact, messaging.DirIn, m.ID}, m: m, now: c.d.Now, consented: true}
	if err := insertMessage(ctx, tx, r); err != nil {
		return ConsentOutcome{}, err
	}
	out.Dispatched = true
	return out, stageImport(ctx, tx, r)
}

// settleHeld moves the pending request to state, with the decision's
// evidence and audit, compared and set on its revision.
func settleHeld(ctx context.Context, tx *sql.Tx, c decided, state string) (ConsentOutcome, error) {
	kind, epoch, gen := evidenceOf(c.d)
	res, err := tx.ExecContext(ctx, `UPDATE consent_requests SET state = ?, revision = revision + 1, decided_at = ?, choice = ?,
    evidence_kind = ?, evidence_epoch = ?, evidence_gen = ?, audit_id = ?, outcome = ?
WHERE id = ? AND state = 'pending' AND revision = ?`,
		state, msgStamp(c.d.Now), c.d.Choice, kind, epoch, gen, nullAudit(c.d.AuditID), settledOutcome[state], c.held.ID, c.held.Revision)
	if err != nil {
		return ConsentOutcome{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ConsentOutcome{}, joinStale(err)
	}
	return ConsentOutcome{ID: c.held.ID, State: state, Revision: c.held.Revision + 1}, nil
}

func joinStale(err error) error {
	if err != nil {
		return err
	}
	return ErrConsentStale
}

// evidenceOf is what a decision persists as its authority: the kind for
// once, session or always, and a session's epoch and generation.
func evidenceOf(d ConsentDecision) (sql.NullString, sql.NullString, sql.NullInt64) {
	if consentChoices[d.Choice] != consentAllowed {
		return sql.NullString{}, sql.NullString{}, sql.NullInt64{}
	}
	if d.Choice != protocol.ConsentSession {
		return nullString(d.Choice), sql.NullString{}, sql.NullInt64{}
	}
	return nullString(d.Choice), nullString(d.Evidence.Epoch), sql.NullInt64{Int64: int64(d.Evidence.Gen), Valid: true}
}

// installGrants installs, for always, the existing grants for exactly the
// scopes shown undecided: a message share per agent; a review grant whose
// publication is at most what the request asked, and only when shown.
func installGrants(ctx context.Context, tx *sql.Tx, c decided) error {
	for _, sc := range c.undecided {
		if err := installGrant(ctx, tx, c, sc); err != nil {
			return err
		}
	}
	granted, err := grantedAgents(ctx, tx, c.held.Contact)
	if err == nil && len(granted) > messaging.MaxAgents {
		err = ErrTooManyGrants
	}
	return err
}

func installGrant(ctx context.Context, tx *sql.Tx, c decided, sc consentScope) error {
	at := msgStamp(c.d.Now)
	switch sc.operation {
	case protocol.ConsentMessage:
		_, err := tx.ExecContext(ctx, `INSERT INTO peer_grants (contact, agent, granted_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
			sc.contact, sc.agent, at)
		return err
	case protocol.ConsentReview:
		publish := slices.Contains(c.undecided, consentScope{sc.contact, sc.agent, protocol.ConsentPublish})
		formal := publish && c.held.Review != nil && c.held.Review.Formal
		_, err := tx.ExecContext(ctx, `INSERT INTO peer_review_grants (contact, agent, mode, publish, formal, granted_at)
VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, sc.contact, sc.agent, ReviewAuto, publish, formal, at)
		return err
	}
	return nil // publication goes with its review's grant, never alone
}
