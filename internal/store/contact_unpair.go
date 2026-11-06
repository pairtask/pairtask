package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// Unpairing a contact (#276) ends its current pairing for good: the
// generation and the global authority mark move on, so every session and
// every exchange begun before it is fenced, and what was queued, staged or
// held under the pairing ends. Nothing is deleted: grants, consent and
// history stay as evidence, and the history stays readable here.

// ErrUnpaired: the contact's pairing was ended; nothing more is sent or
// taken under it (#276).
var ErrUnpaired = errors.New("the contact is unpaired; pair again to resume")

// errAuthorityExhausted is a generation or authority mark that can't move
// on: it never wraps (#276, Amendment B).
var errAuthorityExhausted = errors.New("unpair: the contact's generation or the authority mark is exhausted")

// ContactUnpair ends a contact's current pairing under the operator's
// operation ID and audit row. Confirm must repeat the contact exactly and
// SeenEpoch be the pairing epoch the operator was shown.
type ContactUnpair struct {
	OperationID, Contact, Confirm string
	SeenEpoch                     int64
	AuditID                       int64
	At                            time.Time
}

// ContactUnpairResult is what an unpair did: whether it changed the state,
// the generation after it, the pairing epoch it ended and when.
type ContactUnpairResult struct {
	Changed    bool
	Generation int64
	Epoch      int64
	UnpairedAt time.Time
}

// fingerprint is what the unpair's operation ID stands for.
func (r ContactUnpair) fingerprint() []byte {
	return operationFingerprint("unpair", r.Contact, r.Confirm, strconv.FormatInt(r.SeenEpoch, 10))
}

// UnpairContact ends the contact's current pairing in one transaction with
// its audit; applied is false when the call answered an earlier result.
// The same operation ID for another contact, confirmation, epoch or action
// is ErrConflict, and so is a confirmation that isn't exactly current. An
// unpaired contact again changes nothing.
func (s *Store) UnpairContact(ctx context.Context, r ContactUnpair) (ContactUnpairResult, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContactUnpairResult{}, false, err
	}
	defer tx.Rollback()
	out, replay, err := unpairIn(ctx, tx, r)
	if err != nil {
		return ContactUnpairResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ContactUnpairResult{}, false, err
	}
	return out, out.Changed && !replay, nil
}

// unpairIn is UnpairContact inside its transaction: the first result for a
// retry, else the unpair, then the registry and the audit.
func unpairIn(ctx context.Context, tx *sql.Tx, r ContactUnpair) (ContactUnpairResult, bool, error) {
	op := operation{r.OperationID, "unpair", r.Contact, r.fingerprint(), r.AuditID, r.At}
	if err := requireKnown(ctx, tx, contactKey(r.Contact)); err != nil {
		return ContactUnpairResult{}, false, err
	}
	replay, err := registered(ctx, tx, op)
	if err != nil {
		return ContactUnpairResult{}, false, err
	}
	out, detail, err := unpairOutcome(ctx, tx, r, replay)
	if err != nil {
		return out, replay, err
	}
	if err := register(ctx, tx, op, replay); err != nil {
		return out, replay, err
	}
	return out, replay, finishPauseAudit(ctx, tx, pauseAudit{r.AuditID, detail, replay})
}

// unpairOutcome is a retry's first result, or the unpair made now, with
// what the audit records.
func unpairOutcome(ctx context.Context, tx *sql.Tx, r ContactUnpair, replay bool) (ContactUnpairResult, string, error) {
	if replay {
		out, err := unpairResult(ctx, tx, r.OperationID)
		return out, "unpair (replayed)", err
	}
	c, err := readPairing(ctx, tx, r.Contact)
	if err != nil {
		return ContactUnpairResult{}, "", err
	}
	if r.Confirm != r.Contact || r.SeenEpoch != c.epoch {
		return ContactUnpairResult{}, "", messaging.ErrConflict
	}
	detail := "already unpaired"
	if c.unpairedAt == "" {
		if detail, err = unpairNow(ctx, tx, r, &c); err != nil {
			return ContactUnpairResult{}, "", err
		}
	}
	out, err := recordUnpair(ctx, tx, r.OperationID, c)
	return out, detail, err
}

// pairingRow is the contact's pairing as an unpair reads and changes it.
type pairingRow struct {
	contact           string
	epoch, generation int64
	unpairedAt        string
	pairingID         []byte
	peerVersion       sql.NullInt64
	changed           bool
}

func readPairing(ctx context.Context, tx *sql.Tx, contact string) (pairingRow, error) {
	c := pairingRow{contact: contact}
	err := tx.QueryRowContext(ctx, `SELECT pairing_epoch, generation, unpaired_at, pairing_id, peer_version
FROM peer_contacts WHERE identity_key = ?`, contact).Scan(&c.epoch, &c.generation, &c.unpairedAt, &c.pairingID, &c.peerVersion)
	return c, err
}

// notified: the peer can learn of the unpair, its pairing epoch-capable or
// its messaging new enough (#276).
func (c pairingRow) notified() bool {
	return c.pairingID != nil || c.peerVersion.Valid && c.peerVersion.Int64 >= 9
}

// unpairNow ends the contact's pairing: the contact row and the mark, the
// retired operations, queues and prompts, its running address changes and
// the notice; detail is what the audit records.
func unpairNow(ctx context.Context, tx *sql.Tx, r ContactUnpair, c *pairingRow) (string, error) {
	mark, err := nextMark(ctx, tx, c.generation)
	if err != nil {
		return "", err
	}
	c.generation++
	c.unpairedAt, c.changed = msgStamp(r.At), true
	if err := retireContact(ctx, tx, *c, mark); err != nil {
		return "", err
	}
	detail, err := retireEpoch(ctx, tx, retirement{c.contact, c.unpairedAt, c.epoch})
	if err != nil {
		return "", err
	}
	if err := refuseAddressMoves(ctx, tx, c.contact); err != nil {
		return "", err
	}
	return detail, queueNotice(ctx, tx, *c)
}

// nextMark moves the global authority mark on and answers it; a mark or a
// generation already at its maximum is refused before anything moves.
func nextMark(ctx context.Context, tx *sql.Tx, generation int64) (int64, error) {
	var mark int64
	if err := tx.QueryRowContext(ctx, `SELECT mark FROM pairing_marks WHERE id = 1`).Scan(&mark); err != nil {
		return 0, err
	}
	if mark == math.MaxInt64 || generation == math.MaxInt64 {
		return 0, errAuthorityExhausted
	}
	_, err := tx.ExecContext(ctx, `UPDATE pairing_marks SET mark = ? WHERE id = 1`, mark+1)
	return mark + 1, err
}

// retireContact marks the contact row unpaired at its new generation and
// mark; no exchange begun at or before now may install.
func retireContact(ctx context.Context, tx *sql.Tx, c pairingRow, mark int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET unpaired_at = ?2, paused_at = '', generation = ?3, authority_mark = ?4,
    retired_xseq = COALESCE((SELECT MAX(xseq) FROM pairing_exchanges WHERE identity_key = ?1), retired_xseq)
WHERE identity_key = ?1`, c.contact, c.unpairedAt, c.generation, mark)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE peer_pairing_requests SET state = 'unpaired', operation_id = '' WHERE identity_key = ?`, c.contact)
	return err
}

// retirement is a contact's pairing epoch ending, and when.
type retirement struct {
	contact, at string
	epoch       int64
}

// retireStep is one statement ending what the pairing left open, over
// the contact (?1), the time (?2) and the epoch (?3), each as it needs; a
// labelled step's count goes in the audit.
type retireStep struct{ label, query string }

var retireSteps = []retireStep{
	{"", `UPDATE pairing_exchanges SET state = 'retired', retired_at = ?2 WHERE identity_key = ?1 AND state IN ('open', 'installed')`},
	{"", `UPDATE peer_joins SET retired_at = ?2 WHERE peer = ?1 AND retired_at IS NULL`},
	{"queued ended", `UPDATE peer_messages SET state = 'expired', reason = 'unpaired'
WHERE contact = ?1 AND pairing_epoch = ?3 AND direction = 'out' AND state = 'queued'`},
	{"", `UPDATE peer_messages SET receipt = 'dropped'
WHERE contact = ?1 AND pairing_epoch = ?3 AND direction = 'in' AND receipt = 'pending'`},
	{"imports withheld", `UPDATE remote_imports SET state = 'withheld', reason = 'unpaired', settled_at = ?2
WHERE contact = ?1 AND pairing_epoch = ?3 AND state IN ('pending', 'awaiting')`},
	{"prompts withheld", `UPDATE consent_requests SET state = 'withheld', outcome = 'withheld', outcome_reason = 'unpaired',
    decided_at = ?2, revision = revision + 1 WHERE contact = ?1 AND pairing_epoch = ?3 AND state = 'pending'`},
	{"", `UPDATE consent_held_out SET state = 'ended', reason = 'unpaired', decided_at = ?2
WHERE contact = ?1 AND pairing_epoch = ?3 AND state = 'held'`},
	{"asks ended", `UPDATE review_asks_out SET state = 'contact_removed', reason = 'unpaired', updated_at = ?2, settled_at = ?2
WHERE contact = ?1 AND pairing_epoch = ?3 AND settled_at IS NULL`},
	{"", `UPDATE review_outcome_seq SET refused = 'unpaired', terminal_seq = COALESCE(terminal_seq, seq)
WHERE contact = ?1 AND pairing_epoch = ?3 AND ` + owed + ``},
}

// retireEpoch runs every retire step, and answers the audit's detail with
// the counts it labels.
func retireEpoch(ctx context.Context, tx *sql.Tx, r retirement) (string, error) {
	detail := "unpair"
	for _, step := range retireSteps {
		n, err := step.run(ctx, tx, r)
		if err != nil {
			return "", err
		}
		if step.label != "" {
			detail += "; " + strconv.FormatInt(n, 10) + " " + step.label
		}
	}
	return detail, nil
}

// run is the step for r, and how many rows it ended.
func (s retireStep) run(ctx context.Context, tx *sql.Tx, r retirement) (int64, error) {
	res, err := tx.ExecContext(ctx, s.query, r.contact, r.at, r.epoch)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// refuseAddressMoves settles the contact's running address changes as
// refused: an unpaired contact doesn't move.
func refuseAddressMoves(ctx context.Context, tx *sql.Tx, contact string) error {
	ids, err := queryStrings(ctx, tx, `SELECT message_id FROM contact_address_ops WHERE contact = ? AND state = ?`, contact, AddressRunning)
	if err != nil {
		return err
	}
	for _, id := range ids {
		op, _, err := getAddressOp(ctx, tx, id)
		if err == nil {
			err = settleRefused(ctx, tx, op, ErrUnpaired)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// queueNotice queues the unpaired notice for a peer that can take one.
func queueNotice(ctx context.Context, tx *sql.Tx, c pairingRow) error {
	if !c.notified() {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO contact_notices_out (contact, pairing_id, gen, state) VALUES (?, ?, ?, 'pending')`,
		c.contact, c.pairingID, c.generation)
	return err
}

// recordUnpair keeps the unpair's result for a retry and answers it.
func recordUnpair(ctx context.Context, tx *sql.Tx, operationID string, c pairingRow) (ContactUnpairResult, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO contact_unpair_results (operation_id, changed, generation, pairing_epoch, unpaired_at)
VALUES (?, ?, ?, ?, ?)`, operationID, c.changed, c.generation, c.epoch, c.unpairedAt); err != nil {
		return ContactUnpairResult{}, err
	}
	return unpairResultOf(c.changed, c.generation, c.epoch, c.unpairedAt)
}

// unpairResult is a retried unpair's first result.
func unpairResult(ctx context.Context, tx *sql.Tx, operationID string) (ContactUnpairResult, error) {
	var changed bool
	var generation, epoch int64
	var unpairedAt string
	err := tx.QueryRowContext(ctx, `SELECT changed, generation, pairing_epoch, unpaired_at FROM contact_unpair_results WHERE operation_id = ?`,
		operationID).Scan(&changed, &generation, &epoch, &unpairedAt)
	if err != nil {
		return ContactUnpairResult{}, err
	}
	return unpairResultOf(changed, generation, epoch, unpairedAt)
}

// unpairResultOf is a result from its stored columns.
func unpairResultOf(changed bool, generation, epoch int64, unpairedAt string) (ContactUnpairResult, error) {
	at, err := time.Parse(time.RFC3339Nano, unpairedAt)
	return ContactUnpairResult{Changed: changed, Generation: generation, Epoch: epoch, UnpairedAt: at}, err
}
