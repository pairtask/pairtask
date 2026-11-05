package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// The requester's half of pending consent (#293): a message the contact
// holds for its operator is stored there, so it is delivered here and never
// offered again; its one outcome comes later. An outcome ends the hold for
// good: the same one again changes nothing, another is refused.

// consentHeldOutSchema keeps this side's messages a contact holds, one row
// each, by the message's own ID.
const consentHeldOutSchema = `
CREATE TABLE IF NOT EXISTS consent_held_out (
    contact    TEXT NOT NULL,
    message_id TEXT NOT NULL,
    held_id    TEXT NOT NULL,
    held_at    TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'held',
    reason     TEXT NOT NULL DEFAULT '',
    decided_at TEXT,
    PRIMARY KEY (contact, message_id)
);
`

// heldOut is the state a held message waits in until its outcome.
const heldOut = "held"

// heldOutRow is one held message as stored.
type heldOutRow struct {
	heldID, state, reason string
}

// ended: the hold has its outcome.
func (h heldOutRow) ended() bool { return h.state != heldOut }

// sameOutcome: the outcome the hold already ended in, a replay.
func (h heldOutRow) sameOutcome(o messaging.ConsentOutcome) bool {
	return h.state == o.State && h.reason == o.Reason
}

// HeldBy records the contact's Held for a message this side sent and has
// not had answered: delivered, its outcome to come. A replay of the same
// hold, or any Held once the outcome came, changes nothing.
func (p *PeerMessages) HeldBy(ctx context.Context, contact string, h messaging.Held, at time.Time) error {
	return p.inTx(ctx, contact, func(tx *sql.Tx) error {
		hold, found, err := heldOutFor(ctx, tx, contact, h.ID)
		if err != nil {
			return err
		}
		if found {
			return heldAgain(hold, h)
		}
		if err := requireOffered(ctx, tx, contact, h.ID); err != nil {
			return err
		}
		return recordHold(ctx, tx, contact, h, at)
	})
}

// heldAgain: a Held again for a held message is a replay unless it
// names another hold while still waiting.
func heldAgain(hold heldOutRow, h messaging.Held) error {
	if hold.ended() || hold.heldID == h.HeldID {
		return nil
	}
	return fmt.Errorf("%w: %s already held as another hold", messaging.ErrProtocol, h.ID)
}

// requireOffered: the message is one this side sent and is still offering.
func requireOffered(ctx context.Context, tx *sql.Tx, contact, id string) error {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM peer_messages
WHERE contact = ? AND direction = 'out' AND message_id = ?`, contact, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: held a message never sent", messaging.ErrProtocol)
	}
	if err != nil {
		return err
	}
	if state != messaging.StateQueued && state != messaging.StateExpired {
		return fmt.Errorf("%w: held a message already %s", messaging.ErrProtocol, state)
	}
	return nil
}

// recordHold keeps the hold and delivers the message, as a Stored would.
func recordHold(ctx context.Context, tx *sql.Tx, contact string, h messaging.Held, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO consent_held_out (contact, message_id, held_id, held_at)
VALUES (?, ?, ?, ?)`, contact, h.ID, h.HeldID, msgStamp(at)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE peer_messages SET state = 'delivered',
    delivered_at = COALESCE(delivered_at, ?), class = '', reason = ''
WHERE contact = ? AND direction = 'out' AND message_id = ?`, msgStamp(at), contact, h.ID)
	return err
}

// AcceptConsentOutcome takes the contact's outcome of a message it held:
// the first ends the hold; the same again changes nothing; another is
// ErrConsentTerminalConflict, and one for a message never held ErrProtocol,
// each changing nothing.
func (p *PeerMessages) AcceptConsentOutcome(ctx context.Context, contact string, o messaging.ConsentOutcome, at time.Time) error {
	if _, err := messaging.EncodeConsentOutcome(o); err != nil {
		return err // not an outcome a hold can end in
	}
	return p.inTx(ctx, contact, func(tx *sql.Tx) error {
		hold, found, err := heldOutFor(ctx, tx, contact, o.ID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: consent outcome for a message never held", messaging.ErrProtocol)
		}
		if hold.ended() {
			return replayedOutcome(hold, o)
		}
		return endHold(ctx, tx, contact, o, at)
	})
}

// replayedOutcome: the outcome a hold ended in is a replay; any other,
// even the same state for another reason, contradicts it: refused, never
// applied.
func replayedOutcome(hold heldOutRow, o messaging.ConsentOutcome) error {
	if hold.sameOutcome(o) {
		return nil
	}
	return fmt.Errorf("%w: %s %q for %s, already %s %q", messaging.ErrConsentTerminalConflict, o.State, o.Reason, o.ID, hold.state, hold.reason)
}

func endHold(ctx context.Context, tx *sql.Tx, contact string, o messaging.ConsentOutcome, at time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE consent_held_out SET state = ?, reason = ?, decided_at = ?
WHERE contact = ? AND message_id = ? AND state = 'held'`, o.State, o.Reason, msgStamp(at), contact, o.ID)
	return err
}

// heldOutFor is the hold of this side's message id, if the contact held it.
func heldOutFor(ctx context.Context, tx *sql.Tx, contact, id string) (heldOutRow, bool, error) {
	var h heldOutRow
	err := tx.QueryRowContext(ctx, `SELECT held_id, state, reason FROM consent_held_out
WHERE contact = ? AND message_id = ?`, contact, id).Scan(&h.heldID, &h.state, &h.reason)
	if errors.Is(err, sql.ErrNoRows) {
		return heldOutRow{}, false, nil
	}
	return h, err == nil, err
}

// ConsentOutcomeRefused records the contact's refusal of an outcome this
// side sent (#293): the pending outcome of that request settles as refused,
// with the reason kept, and is never sent again. One not pending, or
// another contact's, changes nothing.
func (p *PeerMessages) ConsentOutcomeRefused(ctx context.Context, contact string, r messaging.ConsentOutcomeRefused) error {
	return p.exec(ctx, contact, `UPDATE consent_requests SET outcome = 'refused', outcome_reason = ?
WHERE contact = ? AND message_id = ? AND outcome = 'pending'`, r.Reason, contact, r.ID)
}
