package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Pending consent (#293). A contact the operator opted in may have a
// request for something never decided here held for the operator instead
// of refused not_shared: one row per request, its whole payload frozen and
// digested at admission, decided once by a compare-and-set on its revision.
// Explicit denials are kept per contact, agent and operation; a contact
// paired before this existed is denied everything until a scope is reset.

var (
	ErrConsentFull     = errors.New("held requests are at their bound for this workspace")
	ErrConsentRate     = errors.New("this workspace sent too many undecided requests; try later")
	ErrConsentConflict = errors.New("the request ID was reused for different content")
	ErrConsentStale    = errors.New("the held request changed or ended since it was shown")
	ErrConsentDenied   = errors.New("a target or operation of this request was explicitly declined")
)

// Consent quotas and lifetime, shared with review ask holds (#293).
const (
	ConsentPerAgent    = 16
	ConsentPerContact  = 64
	ConsentTotal       = 256
	ConsentContactSize = 256 << 10
	ConsentTotalSize   = 2 << 20
	ConsentBurst       = 10
	ConsentRefill      = time.Minute
	ConsentExpiry      = 7 * 24 * time.Hour
)

// consentRequestsColumns is consent_requests with pairing_epoch in its key (#276).
const consentRequestsColumns = `(
    cursor         INTEGER PRIMARY KEY AUTOINCREMENT,
    id             TEXT NOT NULL UNIQUE,
    contact        TEXT NOT NULL,
    pairing_epoch  INTEGER NOT NULL DEFAULT 1,
    message_id     TEXT NOT NULL,
    n              INTEGER NOT NULL,
    created_at     TEXT NOT NULL,
    sender         TEXT NOT NULL,
    recipients     TEXT NOT NULL,
    reply_id       TEXT NOT NULL,
    reply_author   INTEGER NOT NULL,
    text           TEXT,
    review_ask     TEXT,
    hash           BLOB NOT NULL,
    targets        TEXT,
    digest         TEXT NOT NULL,
    bytes          INTEGER NOT NULL,
    state          TEXT NOT NULL,
    revision       INTEGER NOT NULL,
    received_at    TEXT NOT NULL,
    expires_at     TEXT NOT NULL,
    decided_at     TEXT,
    choice         TEXT,
    evidence_kind  TEXT,
    evidence_epoch TEXT,
    evidence_gen   INTEGER,
    audit_id       INTEGER,
    outcome        TEXT NOT NULL DEFAULT '',
    outcome_reason TEXT NOT NULL DEFAULT '',
    refused_reason TEXT NOT NULL DEFAULT '',
    UNIQUE (contact, pairing_epoch, message_id)
)`

// consentDecisionsColumns is consent_decisions with pairing_epoch in its key (#276).
const consentDecisionsColumns = `(
    contact    TEXT NOT NULL,
    pairing_epoch INTEGER NOT NULL DEFAULT 1,
    agent      TEXT NOT NULL,
    operation  TEXT NOT NULL,
    state      TEXT NOT NULL CHECK (state IN ('undecided', 'denied')),
    decided_at TEXT NOT NULL,
    audit_id   INTEGER,
    PRIMARY KEY (contact, pairing_epoch, agent, operation)
)`

// consentSchema keeps held requests, explicit per-scope decisions and each
// contact's arrival budget. A settled request's text, review request and
// targets go NULL once its outcome is answered and it has aged out; its
// identity, sequence, hash and digest stay until the replay floor passes it.
const consentSchema = `
CREATE TABLE IF NOT EXISTS consent_rate (
    contact      TEXT PRIMARY KEY,
    tokens       INTEGER NOT NULL,
    refreshed_at TEXT NOT NULL
);
`

// Held request states, and what a decision may answer.
const (
	consentPending  = "pending"
	consentAllowed  = "allowed"
	consentDeclined = "declined"
	consentExpired  = "expired"
	consentLater    = "later" // an outcome only: the request stays pending
)

// markConsentLegacy marks every contact already paired when consent
// arrives, once, in the migration that adds the column.
func markConsentLegacy(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE peer_contacts SET consent_legacy = 1`)
	return err
}

// ConsentEvidence is what authorizes a decided request: once (this request
// only) or session (only while that operator connection owns the broker).
// The hub builds it; a client never supplies it.
type ConsentEvidence struct {
	Kind  string // protocol.ConsentOnce or protocol.ConsentSession
	Epoch string
	Gen   uint64
}

// authorizes: the evidence lets this one staged request through without a
// grant. A session's is fenced by the hub before it dispatches.
func (e ConsentEvidence) authorizes() bool {
	return e.Kind == protocol.ConsentOnce || e.Kind == protocol.ConsentSession
}

// ConsentDecision is one decision as the hub commits it.
type ConsentDecision struct {
	ID       string
	Digest   string
	Revision int64
	Choice   string
	Evidence ConsentEvidence
	AuditID  int64
	Now      time.Time
}

// ConsentOutcome is what a decision committed.
type ConsentOutcome struct {
	ID         string
	State      string // allowed, declined, later, expired, withheld
	Reason     string
	Revision   int64
	Dispatched bool // its delivery or review was staged in the same transaction
}

// ConsentPageResult is a page of held requests, newest first.
type ConsentPageResult struct {
	Items []protocol.ConsentView
	Total int
	More  bool
	Next  int64
}

// heldOf is the pending requests of contact ?, or of every contact for "".
const heldOf = `state = 'pending' AND (?1 = '' OR contact = ?1)`

// consentPageLimit bounds one ConsentPage.
const consentPageLimit = 64

// ConsentPage lists a contact's held requests, every contact's for "",
// newest first, before a cursor (0 or less for the newest). It only reads:
// nothing is marked, receipted, charged or extended.
func (s *Store) ConsentPage(ctx context.Context, contact string, before int64, limit int) (ConsentPageResult, error) {
	limit = max(1, min(limit, consentPageLimit))
	if before <= 0 {
		before = math.MaxInt64
	}
	var page ConsentPageResult
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM consent_requests WHERE `+heldOf, contact).Scan(&page.Total)
	if err != nil {
		return ConsentPageResult{}, err
	}
	held, err := queryConsent(ctx, s.db, `WHERE `+heldOf+` AND cursor < ? ORDER BY cursor DESC LIMIT ?`, contact, before, limit+1)
	if err != nil {
		return ConsentPageResult{}, err
	}
	page.More = len(held) > limit
	for _, h := range held[:min(len(held), limit)] {
		v, err := consentView(ctx, s.db, h)
		if err != nil {
			return ConsentPageResult{}, err
		}
		page.Items = append(page.Items, v)
		page.Next = v.Cursor
	}
	if !page.More {
		page.Next = 0
	}
	return page, nil
}

// ConsentOptIn turns holding undecided requests from Contact on or off,
// under the operator audit row AuditID, at Now.
type ConsentOptIn struct {
	Contact string
	On      bool
	AuditID int64
	Now     time.Time
}

// SetConsentOptIn turns holding undecided requests from contact on or off.
// Turning it on decides nothing: a legacy contact's scopes stay denied.
func (s *Store) SetConsentOptIn(ctx context.Context, o ConsentOptIn) error {
	detail := map[bool]string{true: "consent on", false: "consent off"}[o.On]
	return s.consentTx(ctx, consentChange{o.Contact, o.AuditID, detail}, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET consent_since = CASE WHEN ?1 THEN COALESCE(consent_since, ?2) END
WHERE identity_key = ?3`, o.On, msgStamp(o.Now), o.Contact)
		return err
	})
}

// ConsentReset makes Agent's Operation for Contact undecided again, under
// the operator audit row AuditID, at Now.
type ConsentReset struct {
	Contact   string
	Agent     string
	Operation string
	AuditID   int64
	Now       time.Time
}

// ResetConsentScope makes one agent's operation for contact undecided again.
func (s *Store) ResetConsentScope(ctx context.Context, r ConsentReset) error {
	if !messaging.ValidAgent(r.Agent) || !consentOperations[r.Operation] {
		return fmt.Errorf("%w: consent scope", messaging.ErrInvalid)
	}
	return s.consentTx(ctx, consentChange{r.Contact, r.AuditID, "consent reset " + r.Operation}, func(tx *sql.Tx) error {
		return decideScope(ctx, tx, scopeDecision{consentScope{r.Contact, r.Agent, r.Operation}, scopeUndecided, r.AuditID, r.Now})
	})
}

// ExpireConsent ends held requests past their lifetime, for good: the same
// request arriving again is not held again.
func (s *Store) ExpireConsent(ctx context.Context, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, expireConsentQuery, msgStamp(now))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// expireConsentQuery settles pending requests past expires_at (?1); a
// caller may narrow it with one more condition on ?2.
const expireConsentQuery = `UPDATE consent_requests SET state = 'expired', decided_at = ?1, revision = revision + 1,
    outcome = 'pending', outcome_reason = 'expired' WHERE state = 'pending' AND expires_at <= ?1`

// expireConsentFor is ExpireConsent for one contact, in its collect pass.
func expireConsentFor(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	_, err := tx.ExecContext(ctx, expireConsentQuery+` AND contact = ?2`, msgStamp(now), contact)
	return err
}

// consentChange is one operator change to a contact's consent, and the
// audit row it finishes.
type consentChange struct {
	contact string
	auditID int64
	detail  string
}

// consentTx runs one consent change on a confirmed contact and finishes its
// audit, in one transaction.
func (s *Store) consentTx(ctx context.Context, c consentChange, apply func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireContact(ctx, tx, contactKey(c.contact)); err != nil {
		return err
	}
	if err := apply(tx); err != nil {
		return err
	}
	if err := finishConsentAudit(ctx, tx, c.auditID, c.detail); err != nil {
		return err
	}
	return tx.Commit()
}

// finishConsentAudit finishes the caller's audit row as accepted; 0 is a
// change no operator asked for, with no row.
func finishConsentAudit(ctx context.Context, tx *sql.Tx, auditID int64, detail string) error {
	if auditID == 0 {
		return nil
	}
	return finishAudit(ctx, tx, auditID, AuditOutcome{Result: AuditAccepted, Detail: detail})
}
