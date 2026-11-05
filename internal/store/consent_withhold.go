package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Work the operator allowed from a hold, once or for a session, is
// authorized by that decision alone; a later explicit denial of any scope
// it needs ends it before dispatch. A decline withholds it in the same
// transaction as the denial, and dispatch checks again, so neither order
// lets it pass. A message is one import for its whole group: withheld, the
// group is; another group without the denied scope is untouched.

// importScopes are the scopes an import's request needs.
func importScopes(imp RemoteImport) []consentScope {
	return targetScopes(imp.Contact, requestTargets(messaging.Message{To: imp.Recipients, Review: imp.Review}))
}

// importDenied: a scope the import needs was explicitly denied since, and
// no grant allows it now.
func importDenied(ctx context.Context, tx *sql.Tx, imp RemoteImport) (bool, error) {
	for _, sc := range importScopes(imp) {
		denied, err := explicitlyDenied(ctx, tx, sc, imp.Review)
		if err != nil || denied {
			return denied, err
		}
	}
	return false, nil
}

// queuedConsentImports are the contact's imports allowed from a hold and
// not yet dispatched.
func queuedConsentImports(ctx context.Context, tx *sql.Tx, contact string) ([]RemoteImport, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+importColumns+importFrom+`
WHERE ri.contact = ? AND ri.state IN ('pending', 'awaiting') AND cr.evidence_kind IN ('once', 'session') ORDER BY ri.rowid`, contact)
	if err != nil {
		return nil, err
	}
	var out []RemoteImport
	for rows.Next() {
		imp, _, err := scanImport(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, imp)
	}
	return out, closeRows(rows)
}

// withholdDenied withholds each of the contact's queued imports allowed
// from a hold that needs a scope explicitly denied now, and queues their
// outcomes.
func withholdDenied(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	return withholdQueued(ctx, tx, queuedEnd{contact, protocol.ConsentDenied, now}, importDenied)
}

// pauseSessions ends a paused contact's session authority: its session
// grants go, and its queued imports allowed for a session are withheld.
func pauseSessions(ctx context.Context, tx *sql.Tx, contact string, now time.Time) error {
	if err := endContactSessions(ctx, tx, contact); err != nil {
		return err
	}
	return withholdQueued(ctx, tx, queuedEnd{contact, protocol.ConsentPaused, now}, sessionImport)
}

// sessionImport: the import was allowed for a session.
func sessionImport(_ context.Context, _ *sql.Tx, imp RemoteImport) (bool, error) {
	return imp.Consent.Kind == protocol.ConsentSession, nil
}

// queuedEnd is a contact's queued imports ending, why, and when.
type queuedEnd struct {
	contact, reason string
	at              time.Time
}

// withholdQueued withholds each of the contact's queued imports allowed
// from a hold that ends says should end, and queues their outcomes.
func withholdQueued(ctx context.Context, tx *sql.Tx, e queuedEnd, ends func(context.Context, *sql.Tx, RemoteImport) (bool, error)) error {
	queued, err := queuedConsentImports(ctx, tx, e.contact)
	if err != nil {
		return err
	}
	for _, imp := range queued {
		end, err := ends(ctx, tx, imp)
		if err == nil && end {
			err = withholdImport(ctx, tx, imp, e)
		}
		if err != nil {
			return err
		}
	}
	return settleConsentOutcomes(ctx, tx, e.contact)
}

// withholdImport settles one queued import withheld, for good, and tells a
// review request's requester.
func withholdImport(ctx context.Context, tx *sql.Tx, imp RemoteImport, e queuedEnd) error {
	_, err := tx.ExecContext(ctx, `UPDATE remote_imports SET state = 'withheld', reason = ?, settled_at = ?
WHERE contact = ? AND remote_id = ? AND state IN ('pending', 'awaiting')`, e.reason, msgStamp(e.at), imp.Contact, imp.RemoteID)
	if err != nil {
		return err
	}
	return withheldAsk(ctx, tx, imp, askEnd{"withheld", e.reason, e.at})
}

// dispatchDenied withholds, at the dispatch boundary, an import allowed
// from a hold whose scope was explicitly denied since; ok is false when
// it may go on.
func dispatchDenied(ctx context.Context, tx *sql.Tx, imp RemoteImport, at importSlot) (ImportResult, bool, error) {
	if !imp.Consent.authorizes() {
		return ImportResult{}, false, nil
	}
	denied, err := importDenied(ctx, tx, imp)
	if err != nil || !denied {
		return ImportResult{}, false, err
	}
	err = withholdImport(ctx, tx, imp, queuedEnd{imp.Contact, protocol.ConsentDenied, at.ts})
	return ImportResult{State: ImportWithheld, Changed: true}, true, err
}
