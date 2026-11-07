package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// The exchange ledger (#276): every exchange writes its row as it commits,
// before any authority write, on the inviter's Commit and the joiner's
// RecordVerified. The row records the exchange's intent and the authority
// mark it began under; a row that can't install is kept as refused, with
// nothing else written. A legacy exchange's row is permitted (state legacy)
// only for a new identity or an active legacy contact's address move
// (Amendment A3); otherwise it is refused as peer_too_old.

// PairingMark is the global authority mark: it moves on at every authority
// transition, so an exchange that began before one can't commit.
func (s *Store) PairingMark(ctx context.Context) (uint64, error) {
	mark, err := readMark(ctx, s.db)
	return uint64(mark), err
}

func readMark(ctx context.Context, q rowQuerier) (int64, error) {
	var mark int64
	err := q.QueryRowContext(ctx, `SELECT mark FROM pairing_marks WHERE id = 1`).Scan(&mark)
	return mark, err
}

// Exchange is one exchange's ledger row, if any.
func (s *Store) Exchange(ctx context.Context, operationID string) (peer.ExchangeRecord, bool, error) {
	return exchangeRow(ctx, s.db, operationID)
}

const exchangeSelect = `SELECT xseq, operation_id, identity_key, role, kind, repair_key, mode, transcript_id, begun_mark, state, decision,
    COALESCE(decided_at, ''), COALESCE(delivered_at, ''), COALESCE(installed_at, ''), COALESCE(retired_at, ''),
    COALESCE(installed_epoch, 0), reason FROM pairing_exchanges`

func exchangeRow(ctx context.Context, q rowQuerier, operationID string) (peer.ExchangeRecord, bool, error) {
	return scanExchange(q.QueryRowContext(ctx, exchangeSelect+` WHERE operation_id = ?`, operationID))
}

func scanExchange(row rowScanner) (peer.ExchangeRecord, bool, error) {
	var x peer.ExchangeRecord
	var mark int64
	var decision string
	times := make([]string, 4)
	err := row.Scan(&x.XSeq, &x.OperationID, &x.IdentityKey, &x.Role, &x.Kind, &x.Repair, &x.Mode, &x.TranscriptID, &mark, &x.State, &decision,
		&times[0], &times[1], &times[2], &times[3], &x.InstalledEpoch, &x.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return peer.ExchangeRecord{}, false, nil
	}
	if err != nil {
		return peer.ExchangeRecord{}, false, err
	}
	x.BegunMark, x.Decision, x.TranscriptID = uint64(mark), decisionState(decision), bytes.Clone(x.TranscriptID)
	err = scanTimes(times, &x.DecidedAt, &x.DeliveredAt, &x.InstalledAt, &x.RetiredAt)
	return x, err == nil, err
}

// scanTimes parses each optional timestamp into its destination.
func scanTimes(texts []string, into ...*time.Time) error {
	for i, text := range texts {
		t, err := optionalTime(text)
		if err != nil {
			return err
		}
		*into[i] = t
	}
	return nil
}

// The ledger's decision column and the request states it mirrors.
var ledgerDecisions = map[string]peer.PairingState{
	"pending": peer.PairingPending, "accepted": peer.PairingAccepted, "rejected": peer.PairingRejected,
}

func decisionState(decision string) peer.PairingState { return ledgerDecisions[decision] }

func ledgerDecision(state peer.PairingState) string {
	for column, s := range ledgerDecisions {
		if s == state {
			return column
		}
	}
	return "pending"
}

// exchangeInput is what an exchange commits with, from either role.
type exchangeInput struct {
	operationID, identity, role string
	mode                        peer.ExchangeMode
	transcript                  []byte
	mark                        uint64
	repair                      string // the declared repair, "" for none
	at                          time.Time
}

func outcomeExchange(o peer.PairingOutcome) exchangeInput {
	return exchangeInput{operationID: o.OperationID, identity: o.IdentityKey, role: "inviter", mode: o.Mode, transcript: o.TranscriptID,
		mark: o.BegunMark, repair: o.Repair, at: o.At}
}

func joinExchange(r peer.JoinRecord) exchangeInput {
	return exchangeInput{operationID: r.OperationID, identity: r.Peer, role: "joiner", mode: r.Mode, transcript: r.TranscriptID,
		mark: r.BegunMark, repair: r.Repair, at: r.UpdatedAt}
}

// kind is the exchange's intent against this side's contact: with an
// active contact, a declared repair or an undeclared exchange (an address
// move); with an unknown or unpaired one, a first or fresh pairing, which
// awaits acceptance whatever it declared.
func (in exchangeInput) kind(c contactAuthority) peer.ExchangeKind {
	if !c.active() {
		return peer.KindPair
	}
	if in.repair != "" {
		return peer.KindRepair
	}
	return peer.KindAddressMove
}

// refusal is why the exchange can't become a candidate, "" if it can: a
// repair of another identity, an authority transition since it began, or
// an address move, which never installs.
func (in exchangeInput) refusal(c contactAuthority) string {
	switch {
	case in.repair != "" && in.repair != in.identity:
		return peer.RefusedMismatch
	case c.mark > int64(in.mark):
		return peer.RefusedCrossed
	case in.kind(c) == peer.KindAddressMove:
		return peer.RefusedAddressMove
	}
	return ""
}

// row is the exchange's ledger row in state, before any refusal.
func (in exchangeInput) row(c contactAuthority, state string) peer.ExchangeRecord {
	return peer.ExchangeRecord{OperationID: in.operationID, IdentityKey: in.identity, Role: in.role, Kind: in.kind(c), Repair: in.repair,
		Mode: in.mode.Stored(), TranscriptID: in.transcript, BegunMark: in.mark, State: state, Decision: peer.PairingPending}
}

// record is the ledger row the epoch-capable exchange commits as.
func (in exchangeInput) record(c contactAuthority) peer.ExchangeRecord {
	x := in.row(c, peer.ExchangeOpen)
	if reason := in.refusal(c); reason != "" {
		x.State, x.Reason = peer.ExchangeRefused, reason
	}
	return x
}

// legacyRecord is the ledger row a legacy exchange commits as: permitted,
// or refused as peer_too_old when Amendment A3 refuses it.
func (in exchangeInput) legacyRecord(c contactAuthority, settled bool) peer.ExchangeRecord {
	x := in.row(c, peer.ExchangeLegacy)
	if in.legacyRefused(c, settled) {
		x.State, x.Reason = peer.ExchangeRefused, peer.RefusedPeerTooOld
	}
	return x
}

// legacyRefused reports a legacy exchange Amendment A3 refuses, checked
// before its allowances: an unpaired or remote-unpaired contact, one with
// a pairing ID, an identity with a retired or installed ledger row (settled),
// or a declared repair. Every re-pair needs the epoch-capable exchange.
func (in exchangeInput) legacyRefused(c contactAuthority, settled bool) bool {
	refused := []bool{c.unpairedAt != "", c.peerState == remoteUnpaired, c.pairingID != nil, settled, in.repair != ""}
	return slices.Contains(refused, true)
}

// remoteUnpaired is the peer_state of a contact the other side unpaired.
const remoteUnpaired = "unpaired"

// matches reports a replay of the exchange recorded as x: every authority
// input the same, the declared repair exactly as it was recorded.
func (in exchangeInput) matches(x peer.ExchangeRecord) bool {
	same := []bool{x.IdentityKey == in.identity, x.Role == in.role, x.Mode == in.mode.Stored(), x.BegunMark == in.mark,
		bytes.Equal(x.TranscriptID, in.transcript), x.Repair == in.repair}
	return !slices.Contains(same, false)
}

// contactAuthority is this side's contact for an identity as the ledger
// judges an exchange by it.
type contactAuthority struct {
	found                            bool
	unpairedAt, pairingOp, peerState string
	epoch, generation                int64
	mark, retiredXSeq                int64
	pairingID                        []byte
}

// active: a contact whose pairing wasn't ended.
func (c contactAuthority) active() bool { return c.found && c.unpairedAt == "" }

func readAuthority(ctx context.Context, q rowQuerier, identity string) (contactAuthority, error) {
	c := contactAuthority{found: true}
	err := q.QueryRowContext(ctx, `SELECT unpaired_at, pairing_op, peer_state, pairing_epoch, generation, authority_mark, retired_xseq, pairing_id
FROM peer_contacts WHERE identity_key = ?`, identity).
		Scan(&c.unpairedAt, &c.pairingOp, &c.peerState, &c.epoch, &c.generation, &c.mark, &c.retiredXSeq, &c.pairingID)
	if errors.Is(err, sql.ErrNoRows) {
		return contactAuthority{}, nil
	}
	return c, err
}

// ledgerRefusal is a refusal whose ledger row is kept: the transaction
// commits, and the call answers the error.
type ledgerRefusal struct{ err error }

func (r ledgerRefusal) Error() string { return r.err.Error() }
func (r ledgerRefusal) Unwrap() error { return r.err }

// commitKept commits tx when err is nil or a kept refusal, and answers err.
func commitKept(tx *sql.Tx, err error) error {
	var kept ledgerRefusal
	if err != nil && !errors.As(err, &kept) {
		return err
	}
	if cerr := tx.Commit(); cerr != nil {
		return cerr
	}
	if err != nil {
		return kept.err
	}
	return nil
}

// refusalOf is the error a committed row answers: none for a candidate or
// an address move, else its refusal, kept.
func refusalOf(x peer.ExchangeRecord) error {
	if x.State != peer.ExchangeRefused {
		return nil
	}
	if err := peer.RefusalError(x.Reason); err != nil {
		return ledgerRefusal{err}
	}
	return nil
}

// commitExchange writes the exchange's ledger row first: refused, with
// nothing else to write, or a candidate. A candidate beside another open
// pairing of the identity refuses both as ambiguous.
func commitExchange(ctx context.Context, tx *sql.Tx, in exchangeInput) (peer.ExchangeRecord, error) {
	c, err := readAuthority(ctx, tx, in.identity)
	if err != nil {
		return peer.ExchangeRecord{}, err
	}
	x := in.record(c)
	if x.State == peer.ExchangeOpen {
		if x, err = refuseCrossing(ctx, tx, x); err != nil {
			return peer.ExchangeRecord{}, err
		}
	}
	if err := insertExchange(ctx, tx, x, in.at); err != nil {
		return peer.ExchangeRecord{}, err
	}
	return x, refusalOf(x)
}

// commitLegacyExchange writes a legacy exchange's ledger row before any
// authority write: permitted, the legacy decision path follows; refused,
// the row is kept and the call answers peer.ErrPeerTooOld.
func commitLegacyExchange(ctx context.Context, tx *sql.Tx, in exchangeInput) (peer.ExchangeRecord, error) {
	c, err := readAuthority(ctx, tx, in.identity)
	if err != nil {
		return peer.ExchangeRecord{}, err
	}
	settled, err := settledIdentity(ctx, tx, in.identity)
	if err != nil {
		return peer.ExchangeRecord{}, err
	}
	x := in.legacyRecord(c, settled)
	if err := insertExchange(ctx, tx, x, in.at); err != nil {
		return peer.ExchangeRecord{}, err
	}
	return x, refusalOf(x)
}

// settledIdentity: the identity has a retired or installed ledger row.
func settledIdentity(ctx context.Context, q rowQuerier, identity string) (bool, error) {
	var found bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pairing_exchanges WHERE identity_key = ? AND state IN ('retired', 'installed'))`,
		identity).Scan(&found)
	return found, err
}

// refuseCrossing refuses x and every other open pairing of its identity as
// ambiguous, when there is one; only the display mirror of a refused
// candidate is dropped (#276, D7).
func refuseCrossing(ctx context.Context, tx *sql.Tx, x peer.ExchangeRecord) (peer.ExchangeRecord, error) {
	rivals, err := queryStrings(ctx, tx, `SELECT operation_id FROM pairing_exchanges WHERE identity_key = ? AND state = 'open'`, x.IdentityKey)
	if err != nil || len(rivals) == 0 {
		return x, err
	}
	for _, op := range rivals {
		if err := refuseExchange(ctx, tx, op, peer.RefusedAmbiguous); err != nil {
			return x, err
		}
	}
	x.State, x.Reason = peer.ExchangeRefused, peer.RefusedAmbiguous
	return x, nil
}

func insertExchange(ctx context.Context, tx *sql.Tx, x peer.ExchangeRecord, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO pairing_exchanges
(operation_id, identity_key, role, kind, repair_key, transcript_id, begun_mark, state, mode, decision, reason, recorded_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, x.OperationID, x.IdentityKey, x.Role, string(x.Kind), x.Repair, x.TranscriptID,
		int64(x.BegunMark), x.State, string(x.Mode), x.Reason, stamp(at))
	return err
}

// refuseExchange refuses an open exchange for reason and drops it from the
// request's display mirror.
func refuseExchange(ctx context.Context, tx *sql.Tx, operationID, reason string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE pairing_exchanges SET state = 'refused', reason = ? WHERE operation_id = ? AND state = 'open'`,
		reason, operationID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE peer_pairing_requests SET operation_id = '' WHERE operation_id = ?`, operationID)
	return err
}

// exchangeDecision is a decision on one exchange, and when.
type exchangeDecision struct {
	operationID string
	decision    peer.PairingState
	at          time.Time
}

// decideExchange records the decision on an open exchange, once.
func decideExchange(ctx context.Context, tx *sql.Tx, d exchangeDecision) error {
	_, err := tx.ExecContext(ctx, `UPDATE pairing_exchanges SET decision = ?, decided_at = COALESCE(decided_at, ?)
WHERE operation_id = ? AND state = 'open'`, ledgerDecision(d.decision), stamp(d.at), d.operationID)
	return err
}

// epochIdentity: the identity has an epoch-capable exchange, so its
// deliveries must name their operation.
func epochIdentity(ctx context.Context, q rowQuerier, identity string) (bool, error) {
	var found bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pairing_exchanges WHERE identity_key = ? AND mode = 'epoch')`, identity).Scan(&found)
	return found, err
}
