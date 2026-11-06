package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// A held request's end goes back to its requester durably, by its own
// message ID: queued when the request is declined or expires, until the
// requester answers it. An allowed request's outcome is its dispatch's, not
// the decision's: allowed when the import it staged is imported, withheld
// when that import is withheld instead, queued in that same transaction.
// Either is its last: a review's later lifecycle never rewrites it. Why it
// ended is written with it and sent as written, whatever is pruned later.

// settledOutcome is whether settling a request in a state queues its
// outcome: pending to send, or "" for none.
var settledOutcome = map[string]string{consentDeclined: "pending"}

// settledReasons are why a decision ended a request, by its state.
var settledReasons = map[string]string{consentDeclined: protocol.ConsentDenied}

// consentWithheld is an allowed request whose import was withheld before
// dispatch: its session ended, or a grant or scope it needed was revoked.
const consentWithheld = "withheld"

// settleConsentOutcomes queues the outcome of each of contact's allowed
// requests whose import settled in this transaction: allowed when it was
// imported, withheld when it was withheld, with the import's reason as the
// request's own. Each is queued once.
func settleConsentOutcomes(ctx context.Context, tx *sql.Tx, contact string) error {
	_, err := tx.ExecContext(ctx, `UPDATE consent_requests SET outcome = 'pending',
    state = CASE WHEN (`+settledImport+`) = 'withheld' THEN 'withheld' ELSE state END,
    outcome_reason = CASE WHEN (`+settledImport+`) = 'withheld' THEN (`+withheldReason+`) ELSE '' END
WHERE contact = ?1 AND state = 'allowed' AND outcome = '' AND (`+settledImport+`) IN ('imported', 'withheld')`, contact)
	return err
}

// settledImport is the state of the import an allowed request staged;
// withheldReason is why it was withheld.
const (
	settledImport = `SELECT ri.state FROM remote_imports ri
    WHERE ri.contact = consent_requests.contact AND ri.remote_id = consent_requests.message_id`
	withheldReason = `SELECT COALESCE(ri.reason, '') FROM remote_imports ri
    WHERE ri.contact = consent_requests.contact AND ri.remote_id = consent_requests.message_id`
)

// WithholdConsent settles, for good, an import staged under session
// evidence the hub found ended: nothing is delivered or dispatched, and
// the requester hears it was withheld, in one transaction. One no longer
// pending is answered as it is.
func (s *Store) WithholdConsent(ctx context.Context, imp RemoteImport, reason string, now time.Time) (ImportResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, err
	}
	defer tx.Rollback()
	ids, err := queryStrings(ctx, tx, `UPDATE remote_imports SET state = 'withheld', reason = ?1, settled_at = ?2
WHERE contact = ?3 AND remote_id = ?4 AND state = 'pending' RETURNING remote_id`, reason, msgStamp(now), imp.Contact, imp.RemoteID)
	if err != nil || len(ids) == 0 {
		return ImportResult{}, err
	}
	if err := withheldAsk(ctx, tx, imp, askEnd{"withheld", reason, now}); err != nil {
		return ImportResult{}, err
	}
	if err := settleConsentOutcomes(ctx, tx, imp.Contact); err != nil {
		return ImportResult{}, err
	}
	return ImportResult{State: ImportWithheld, Changed: true}, tx.Commit()
}

// withheldAsk tells a review request's requester its request was withheld,
// as any request withheld before dispatch is.
func withheldAsk(ctx context.Context, tx *sql.Tx, imp RemoteImport, e askEnd) error {
	if !imp.IsReview() {
		return nil
	}
	return askOutcome(ctx, tx, imp, e)
}

// ConsentMark is where held requests stand: how many wait, and the newest.
// A change in it is an arrival or an end the operator should hear of.
type ConsentMark struct {
	Pending int
	Newest  int64
}

// ConsentMark reads where held requests stand now.
func (s *Store) ConsentMark(ctx context.Context) (ConsentMark, error) {
	var m ConsentMark
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(cursor), 0) FROM consent_requests WHERE state = 'pending'`).Scan(&m.Pending, &m.Newest)
	return m, err
}

// PendingConsentOutcomes is the oldest outcomes of the contact's held
// requests its requester hasn't answered yet, each as it was settled.
func (p *PeerMessages) PendingConsentOutcomes(ctx context.Context, contact string, max int) ([]messaging.ConsentOutcome, error) {
	var out []messaging.ConsentOutcome
	err := p.inTx(ctx, contact, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT message_id, state, outcome_reason FROM consent_requests
WHERE contact = ? AND outcome = 'pending' ORDER BY decided_at, cursor LIMIT ?`, contact, sessionBudget.clamp(budget{max, 0}).count)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o messaging.ConsentOutcome
			if err := rows.Scan(&o.ID, &o.State, &o.Reason); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

// ConsentOutcomeAnswered records the requester's answer to an outcome; one
// it never had, or answered before, changes nothing.
func (p *PeerMessages) ConsentOutcomeAnswered(ctx context.Context, contact string, s messaging.ConsentOutcomeStored) error {
	return p.exec(ctx, contact, `UPDATE consent_requests SET outcome = 'answered'
WHERE contact = ? AND message_id = ? AND outcome = 'pending'`, contact, s.ID)
}
