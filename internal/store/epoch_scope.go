package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// currentEpoch is a contact's current pairing epoch, from its one
// parameter, the contact: 1 for a key with no contact row. Rows written by
// an upsert into a table keyed by epoch land in it (#276).
const currentEpoch = `COALESCE((SELECT ce.pairing_epoch FROM peer_contacts ce WHERE ce.identity_key = ?), 1)`

// currentEpochOf1 is currentEpoch for a statement with numbered
// parameters, the contact its first.
const currentEpochOf1 = `COALESCE((SELECT ce.pairing_epoch FROM peer_contacts ce WHERE ce.identity_key = ?1), 1)`

// currentEpochAt is currentEpoch for a statement with numbered
// parameters, the contact its nth.
func currentEpochAt(n int) string {
	return fmt.Sprintf(`COALESCE((SELECT ce.pairing_epoch FROM peer_contacts ce WHERE ce.identity_key = ?%d), 1)`, n)
}

// currentEpochFor is the current pairing epoch of the contact a row
// names in column, for a statement over many contacts' rows: a cap counts
// each contact's current pairing alone (#276).
func currentEpochFor(column string) string {
	return `COALESCE((SELECT ce.pairing_epoch FROM peer_contacts ce WHERE ce.identity_key = ` + column + `), 1)`
}

// errPairingEnded refuses work from a pairing the contact has since ended
// and paired again: the new pairing's authority never covers it (#276).
var errPairingEnded = fmt.Errorf("%w: this came under an earlier pairing with the contact", ErrUnpaired)

// pairingKey is one pairing with a contact: the contact and its epoch.
type pairingKey struct {
	contact string
	epoch   int64
}

// contactEpoch is a contact's current pairing epoch, 1 with no contact row.
func contactEpoch(ctx context.Context, q rowQuerier, contact string) (int64, error) {
	var epoch int64
	err := q.QueryRowContext(ctx, `SELECT `+currentEpoch, contact).Scan(&epoch)
	return epoch, err
}

// currentPairing is a contact's current pairing.
func currentPairing(ctx context.Context, q rowQuerier, contact string) (pairingKey, error) {
	epoch, err := contactEpoch(ctx, q, contact)
	return pairingKey{contact, epoch}, err
}

// historyEpochOf1 is the newest pairing the history of contact ?1 holds:
// its current one, or past it the newest a kept message came under, as
// for a contact removed after pairing again; 1 with neither. History reads
// default to it; it never names authority, which contactEpoch and the
// contact row keep (#276).
const historyEpochOf1 = `MAX(` + currentEpochOf1 + `,
    COALESCE((SELECT MAX(he.pairing_epoch) FROM peer_messages he WHERE he.contact = ?1), 1))`

// historyEpochFor is historyEpochOf1 for the contact a row names in
// column, for a statement over many contacts' rows.
func historyEpochFor(column string) string {
	return `MAX(` + currentEpochFor(column) + `,
    COALESCE((SELECT MAX(he.pairing_epoch) FROM peer_messages he WHERE he.contact = ` + column + `), 1))`
}

// historyEpoch is the newest pairing a contact's history holds: the bound
// of every pairing a history read may name, and the one it reads by
// default.
func historyEpoch(ctx context.Context, q rowQuerier, contact string) (int64, error) {
	var epoch int64
	err := q.QueryRowContext(ctx, `SELECT `+historyEpochOf1, contact).Scan(&epoch)
	return epoch, err
}

// ContactEpoch is the newest pairing with the contact its history holds
// (#276): the current one, or for a contact removed since, the newest its
// kept messages came under. It is what a history read names by default,
// never authority to send or receive under it.
func (s *Store) ContactEpoch(ctx context.Context, contact string) (int64, error) {
	return historyEpoch(ctx, s.db, contact)
}

// errNoSuchPairing names, for history, a pairing epoch the contact never
// had: none before 1, none past the newest its history holds.
var errNoSuchPairing = fmt.Errorf("%w: no such pairing with the contact", messaging.ErrInvalid)

// heldPairing checks k names one of the pairings its contact's history
// holds, the current one or an earlier, and reports whether it is live:
// the contact's pairing now, and not unpaired (#276). A contact with no
// row has none live, whatever its history holds.
func heldPairing(ctx context.Context, q rowQuerier, k pairingKey) (bool, error) {
	if k.epoch <= 0 {
		return false, errNoSuchPairing
	}
	bound, err := historyEpoch(ctx, q, k.contact)
	if err != nil {
		return false, err
	}
	if k.epoch > bound {
		return false, errNoSuchPairing
	}
	return livePairing(ctx, q, k)
}

// namedPairing is the pairing an operation names by k's epoch: the
// contact's current one for 0, else one its history holds (#276).
func namedPairing(ctx context.Context, q rowQuerier, k pairingKey) (pairingKey, error) {
	if k.epoch == 0 {
		return currentPairing(ctx, q, k.contact)
	}
	_, err := heldPairing(ctx, q, k)
	return k, err
}

// livePairing: k is its contact's pairing now, with a contact row that
// isn't unpaired.
func livePairing(ctx context.Context, q rowQuerier, k pairingKey) (bool, error) {
	var live bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM peer_contacts
WHERE identity_key = ? AND pairing_epoch = ? AND unpaired_at = '')`, k.contact, k.epoch).Scan(&live)
	return live, err
}

// MessageKey names one message of one pairing with a contact, for a
// history read (#276): the contact, the pairing's epoch, 0 for the newest
// the history holds, and the message's ID.
type MessageKey struct {
	Contact string
	Epoch   int64
	ID      string
}

// inHistory runs a read-only lookup of the message k names in the pairing
// it names, both from one snapshot (#276).
func inHistory[T any](ctx context.Context, db *sql.DB, k MessageKey, read func(*sql.Tx, pairingKey) (T, bool, error)) (T, bool, error) {
	var none T
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return none, false, err
	}
	defer tx.Rollback()
	pk, err := historyPairing(ctx, tx, k)
	if err != nil {
		return none, false, err
	}
	return read(tx, pk)
}

// historyPairing is the pairing a history read of k names: its epoch as
// given, or with 0 the newest the history holds. One the contact never
// had is messaging.ErrInvalid.
func historyPairing(ctx context.Context, q rowQuerier, k MessageKey) (pairingKey, error) {
	if k.Epoch < 0 {
		return pairingKey{}, errNoSuchPairing
	}
	bound, err := historyEpoch(ctx, q, k.Contact)
	switch {
	case err != nil:
		return pairingKey{}, err
	case k.Epoch > bound:
		return pairingKey{}, errNoSuchPairing
	case k.Epoch == 0:
		return pairingKey{k.Contact, bound}, nil
	}
	return pairingKey{k.Contact, k.Epoch}, nil
}

// remoteEpoch is the pairing epoch a review a paired workspace asked for
// came under; one stored before epochs were kept came under the first.
func remoteEpoch(ctx context.Context, q rowQuerier, reviewID string) (int64, error) {
	var epoch int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(remote_epoch, 1) FROM reviews WHERE review_id = ?`, reviewID).Scan(&epoch)
	return epoch, err
}

// refuseRetired refuses work from a pairing that has ended: the contact
// unpaired, or paired again since. A key with no contact row passes.
func refuseRetired(ctx context.Context, q rowQuerier, k pairingKey) error {
	var unpairedAt string
	var epoch int64
	err := q.QueryRowContext(ctx, `SELECT unpaired_at, pairing_epoch FROM peer_contacts WHERE identity_key = ?`, k.contact).Scan(&unpairedAt, &epoch)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	case unpairedAt != "":
		return ErrUnpaired
	case epoch != k.epoch:
		return errPairingEnded
	}
	return nil
}

// livePairingImport is an import, as ri, staged under its contact's
// current pairing, which hasn't ended: a key with no contact row passes.
const livePairingImport = `NOT EXISTS (SELECT 1 FROM peer_contacts lp WHERE lp.identity_key = ri.contact
    AND (lp.unpaired_at != '' OR lp.pairing_epoch != ri.pairing_epoch))`

// source is the pairing an import was staged under; 0 is the legacy 1.
func (imp RemoteImport) source() pairingKey {
	return pairingKey{imp.Contact, max(imp.Epoch, 1)}
}

// stampNotice keeps, with a notice written here of a paired workspace's
// review request or its outcome, where it came from and the pairing epoch,
// so a replay still says whom to answer (#259).
func stampNotice(ctx context.Context, tx *sql.Tx, msg protocol.Message, epoch int64) error {
	if msg.Remote == nil {
		return stampSource(ctx, tx, msg.MessageID, epoch)
	}
	_, err := tx.ExecContext(ctx, `UPDATE message_metadata SET remote_contact = ?, remote_sender = ?, remote_epoch = ? WHERE message_id = ?`,
		msg.Remote.Contact, msg.Remote.Sender, epoch, msg.MessageID)
	return err
}

// stampSource keeps, with a message an import became, the pairing epoch
// the import came under (#276).
func stampSource(ctx context.Context, tx *sql.Tx, messageID string, epoch int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE message_metadata SET remote_epoch = ? WHERE message_id = ?`, epoch, messageID)
	return err
}

// pairingScope is the pairings an operator review page reads of the
// requests it lists, as a condition on the request a: "" for every one
// (#276).
type pairingScope string

// allPairings is every pairing; currentPairings is the newest with each
// contact its history holds, as CurrentAsksBy reads: what an older client
// reads.
var (
	allPairings     pairingScope
	currentPairings = pairingScope(`a.pairing_epoch = ` + historyEpochFor("a.contact"))
)

// where is the scope and the other conditions as a WHERE clause, "" with
// none.
func (s pairingScope) where(conds ...string) string {
	if s != allPairings {
		conds = append([]string{string(s)}, conds...)
	}
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// and is the scope as a further condition, "" for every pairing.
func (s pairingScope) and() string {
	if s == allPairings {
		return ""
	}
	return " AND " + string(s)
}
