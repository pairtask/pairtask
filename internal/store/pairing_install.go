package store

import (
	"context"
	"database/sql"
	"math"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// Installing a pairing (#276): the inviter's DeliverExchange or the
// joiner's RecordDecisionConfirmed delivers one exchange by its operation.
// A replay answers what the ledger recorded and writes nothing; only then
// is the authority mark compared, and an accepted candidate installs, in
// one transaction, a fresh pairing epoch with its own pairing ID: a repair
// retires the epoch it replaces first.

// DeliverExchange records the joiner's acknowledgement of one exchange's
// decision; see peer.PairingStore.
func (s *Store) DeliverExchange(ctx context.Context, d peer.ExchangeDelivery) (peer.InstallResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return peer.InstallResult{}, err
	}
	defer tx.Rollback()
	result, err := deliverExchange(ctx, tx, d)
	if err = commitKept(tx, err); err != nil {
		return peer.InstallResult{}, err
	}
	return result, nil
}

// deliverExchange is DeliverExchange inside its transaction: only the
// inviter's own exchange with that identity is delivered.
func deliverExchange(ctx context.Context, tx *sql.Tx, d peer.ExchangeDelivery) (peer.InstallResult, error) {
	x, found, err := exchangeRow(ctx, tx, d.OperationID)
	if err != nil {
		return peer.InstallResult{}, err
	}
	if !found || x.Role != "inviter" {
		return peer.InstallResult{}, peer.ErrNoPairingRequest
	}
	if x.IdentityKey != d.IdentityKey {
		return peer.InstallResult{}, peer.ErrPeerMismatch
	}
	contact, err := inviterContact(ctx, tx, x.IdentityKey, d.At)
	if err != nil {
		return peer.InstallResult{}, err
	}
	return settleExchange(ctx, tx, installing{x: x, decision: d.Decision, at: d.At, contact: contact})
}

// inviterContact is the contact an inviter's install makes, from the
// identity's request.
func inviterContact(ctx context.Context, tx *sql.Tx, identity string, at time.Time) (peer.Contact, error) {
	request, err := pairingRequest(ctx, tx, identity)
	if err != nil {
		return peer.Contact{}, err
	}
	request.DeliveredAt = at
	return peer.InviterContact(request), nil
}

// installing is one exchange's delivery: its ledger row, the decision
// delivered and when, and the contact an install makes.
type installing struct {
	x        peer.ExchangeRecord
	decision peer.PairingState
	at       time.Time
	contact  peer.Contact
}

// settleExchange answers a replay from the ledger row first: an installed
// row as recorded, a retired one with its history, a refused one with its
// refusal. Only an open row is delivered.
func settleExchange(ctx context.Context, tx *sql.Tx, in installing) (peer.InstallResult, error) {
	switch in.x.State {
	case peer.ExchangeInstalled:
		return installedResult(in)
	case peer.ExchangeRetired:
		return historicalResult(in.x), nil
	case peer.ExchangeRefused:
		return refusedResult(ctx, tx, in.x)
	}
	return deliverOpen(ctx, tx, in)
}

// installedResult is an installed row's replay: its decision must be the
// one delivered.
func installedResult(in installing) (peer.InstallResult, error) {
	if in.decision != peer.PairingAccepted {
		return peer.InstallResult{}, peer.ErrNotPending
	}
	return historicalResult(in.x), nil
}

// historicalResult is what a row installed, if anything; nothing moves.
func historicalResult(x peer.ExchangeRecord) peer.InstallResult {
	result := peer.InstallResult{Exchange: x, Epoch: x.InstalledEpoch}
	if x.InstalledEpoch != 0 {
		result.PairingID = peer.PairingID(x.TranscriptID)
	}
	return result
}

// refusedResult is a refused row's error; an address move, settled without
// installing, answers the contact's current pairing.
func refusedResult(ctx context.Context, tx *sql.Tx, x peer.ExchangeRecord) (peer.InstallResult, error) {
	if err := peer.RefusalError(x.Reason); err != nil {
		return peer.InstallResult{}, err
	}
	c, err := readAuthority(ctx, tx, x.IdentityKey)
	return peer.InstallResult{Exchange: x, Epoch: c.epoch, PairingID: c.pairingID}, err
}

// deliverOpen delivers an open candidate's decision: a rejection ends it,
// an acceptance installs it unless an authority transition crossed it.
func deliverOpen(ctx context.Context, tx *sql.Tx, in installing) (peer.InstallResult, error) {
	if in.decision != in.x.Decision {
		return peer.InstallResult{}, peer.ErrNotPending
	}
	if in.decision != peer.PairingAccepted {
		return endRejected(ctx, tx, in)
	}
	c, err := readAuthority(ctx, tx, in.x.IdentityKey)
	if err != nil {
		return peer.InstallResult{}, err
	}
	if crossed(in.x, c) {
		if err := refuseExchange(ctx, tx, in.x.OperationID, peer.RefusedCrossed); err != nil {
			return peer.InstallResult{}, err
		}
		return peer.InstallResult{}, ledgerRefusal{peer.ErrCrossedTransition}
	}
	return install(ctx, tx, in, c)
}

// crossed: an authority transition since the exchange began, or an unpair
// that retired every exchange up to it.
func crossed(x peer.ExchangeRecord, c contactAuthority) bool {
	return c.mark > int64(x.BegunMark) || x.XSeq <= c.retiredXSeq
}

// endRejected records a rejection's delivery: the candidate is retired,
// having installed nothing.
func endRejected(ctx context.Context, tx *sql.Tx, in installing) (peer.InstallResult, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE pairing_exchanges SET state = 'retired', reason = 'rejected', delivered_at = ?2, retired_at = ?2
WHERE operation_id = ?1`, in.x.OperationID, stamp(in.at)); err != nil {
		return peer.InstallResult{}, err
	}
	if err := mirrorDelivery(ctx, tx, in); err != nil {
		return peer.InstallResult{}, err
	}
	return installedRow(ctx, tx, in.x.OperationID, 0)
}

// install retires the epoch a repair replaces, makes or merges the
// contact, and installs the exchange's pairing at the next epoch with a
// moved generation and mark.
func install(ctx context.Context, tx *sql.Tx, in installing, c contactAuthority) (peer.InstallResult, error) {
	if err := retireReplaced(ctx, tx, in, c); err != nil {
		return peer.InstallResult{}, err
	}
	if err := upsertContact(ctx, tx, in.contact, in.x.OperationID); err != nil {
		return peer.InstallResult{}, err
	}
	epoch, err := nextEpoch(c)
	if err != nil {
		return peer.InstallResult{}, err
	}
	mark, err := nextMark(ctx, tx, c.generation)
	if err != nil {
		return peer.InstallResult{}, err
	}
	if err := installContact(ctx, tx, installedPairing{in.x, epoch, mark}); err != nil {
		return peer.InstallResult{}, err
	}
	if err := installExchange(ctx, tx, in, epoch); err != nil {
		return peer.InstallResult{}, err
	}
	return installedRow(ctx, tx, in.x.OperationID, epoch)
}

// nextEpoch is the epoch an install makes: the next one of a known
// contact, 1 for a new one; it never wraps.
func nextEpoch(c contactAuthority) (int64, error) {
	if !c.found {
		return 1, nil
	}
	if c.epoch == math.MaxInt64 {
		return 0, errAuthorityExhausted
	}
	return c.epoch + 1, nil
}

// retireReplaced retires the active pairing a repair replaces, as an
// unpair would, but for the repair itself.
func retireReplaced(ctx context.Context, tx *sql.Tx, in installing, c contactAuthority) error {
	if in.x.Kind != peer.KindRepair || !c.active() {
		return nil
	}
	r := retirement{contact: in.x.IdentityKey, at: msgStamp(in.at), epoch: c.epoch, reason: "replaced", except: in.x.OperationID}
	if _, err := retireEpoch(ctx, tx, r); err != nil {
		return err
	}
	return refuseAddressMoves(ctx, tx, r.contact)
}

// installedPairing is the pairing an install puts on the contact.
type installedPairing struct {
	x           peer.ExchangeRecord
	epoch, mark int64
}

// installContact makes the contact's pairing the installed one, with
// nothing of the epoch before: no directory, no remote state, no legacy
// consent; every epoch-keyed row starts empty at the new epoch.
func installContact(ctx context.Context, tx *sql.Tx, p installedPairing) error {
	_, err := tx.ExecContext(ctx, `UPDATE peer_contacts SET pairing_epoch = ?, pairing_id = ?, pairing_op = ?, unpaired_at = '',
    peer_state = '', directory_at = NULL, consent_legacy = 0, generation = generation + 1, authority_mark = ?
WHERE identity_key = ?`, p.epoch, peer.PairingID(p.x.TranscriptID), p.x.OperationID, p.mark, p.x.IdentityKey)
	return err
}

// installExchange marks the row installed, immutable from now on but for
// its retirement, and mirrors the delivery on the request.
func installExchange(ctx context.Context, tx *sql.Tx, in installing, epoch int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE pairing_exchanges SET state = 'installed', delivered_at = ?2, installed_at = ?2, installed_epoch = ?3
WHERE operation_id = ?1`, in.x.OperationID, stamp(in.at), epoch); err != nil {
		return err
	}
	return mirrorDelivery(ctx, tx, in)
}

// mirrorDelivery shows the delivery on the inviter's request, display only.
func mirrorDelivery(ctx context.Context, tx *sql.Tx, in installing) error {
	_, err := tx.ExecContext(ctx, `UPDATE peer_pairing_requests SET delivered_at = ? WHERE identity_key = ? AND operation_id = ?`,
		stamp(in.at), in.x.IdentityKey, in.x.OperationID)
	return err
}

// installedRow is the delivered row's result as written.
func installedRow(ctx context.Context, tx *sql.Tx, operationID string, epoch int64) (peer.InstallResult, error) {
	x, _, err := exchangeRow(ctx, tx, operationID)
	if err != nil {
		return peer.InstallResult{}, err
	}
	result := historicalResult(x)
	result.Changed, result.Epoch = true, epoch
	return result, nil
}
