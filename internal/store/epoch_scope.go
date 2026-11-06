package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// stampSource keeps, with a message an import became, the pairing epoch
// the import came under (#276).
func stampSource(ctx context.Context, tx *sql.Tx, messageID string, epoch int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE message_metadata SET remote_epoch = ? WHERE message_id = ?`, epoch, messageID)
	return err
}
