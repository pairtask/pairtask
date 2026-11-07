package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// peerPairingSchema keeps one decision row per identity and one immutable row
// per successful pairing operation (#178). The identity key is a pairtask SPKI
// fingerprint, see peer.PairingRequest; names are metadata only. Rows written
// before the bounds stay as they are.
const peerPairingSchema = `
CREATE TABLE IF NOT EXISTS peer_pairing_requests (
    identity_key    TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    state           TEXT NOT NULL,
    first_paired_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS peer_pairing_outcomes (
    operation_id  TEXT PRIMARY KEY,
    invitation_id TEXT NOT NULL UNIQUE,
    identity_key  TEXT NOT NULL REFERENCES peer_pairing_requests(identity_key),
    name          TEXT NOT NULL,
    transcript_id BLOB NOT NULL,
    at            TEXT NOT NULL
);
`

var _ peer.PairingStore = (*Store)(nil)

// Commit writes the outcome and its request in one transaction; see
// peer.PairingStore. Every outcome writes its ledger row first; a refusal
// keeps only that row, and still answers its error.
func (s *Store) Commit(ctx context.Context, o peer.PairingOutcome) (peer.PairingRequest, error) {
	if err := peer.ValidOutcome(o); err != nil {
		return peer.PairingRequest{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return peer.PairingRequest{}, err
	}
	defer tx.Rollback()
	request, err := commitPairing(ctx, tx, o)
	if err = commitKept(tx, err); err != nil {
		return peer.PairingRequest{}, fmt.Errorf("commit pairing: %w", err)
	}
	return request, nil
}

func commitPairing(ctx context.Context, tx *sql.Tx, o peer.PairingOutcome) (peer.PairingRequest, error) {
	x, found, err := exchangeRow(ctx, tx, o.OperationID)
	if err != nil {
		return peer.PairingRequest{}, err
	}
	if found {
		return replayExchange(ctx, tx, x, o)
	}
	known, found, err := pairingOutcome(ctx, tx, o.OperationID)
	if err != nil {
		return peer.PairingRequest{}, err
	}
	if found {
		return replayPairing(ctx, tx, known, o)
	}
	if err := admitPairing(ctx, tx, o); err != nil {
		return peer.PairingRequest{}, err
	}
	if o.Mode.Epoch() {
		return commitEpoch(ctx, tx, o)
	}
	return commitLegacy(ctx, tx, o)
}

// commitLegacy commits a legacy outcome: its ledger row, then, if Amendment
// A3 permits it, the request and outcome as before; a refusal keeps only
// the row.
func commitLegacy(ctx context.Context, tx *sql.Tx, o peer.PairingOutcome) (peer.PairingRequest, error) {
	if _, err := commitLegacyExchange(ctx, tx, outcomeExchange(o)); err != nil {
		return peer.PairingRequest{}, err
	}
	if err := insertPairing(ctx, tx, o); err != nil {
		return peer.PairingRequest{}, err
	}
	return pairingRequest(ctx, tx, o.IdentityKey)
}

// replayPairing answers a committed operation: identical content returns its
// request, anything else conflicts. It runs before any newer check, so rows
// written before the bounds replay unchanged.
func replayPairing(ctx context.Context, tx *sql.Tx, known, o peer.PairingOutcome) (peer.PairingRequest, error) {
	if !peer.SameOutcome(known, o) || o.Mode.Epoch() {
		return peer.PairingRequest{}, peer.ErrOutcomeConflict
	}
	return pairingRequest(ctx, tx, o.IdentityKey)
}

// replayExchange answers an operation the ledger holds: the same authority
// inputs and outcome answer the identity's request as it is now, or the
// row's refusal; anything else conflicts. Nothing is written.
func replayExchange(ctx context.Context, tx *sql.Tx, x peer.ExchangeRecord, o peer.PairingOutcome) (peer.PairingRequest, error) {
	if !outcomeExchange(o).matches(x) {
		return peer.PairingRequest{}, peer.ErrOutcomeConflict
	}
	known, found, err := pairingOutcome(ctx, tx, o.OperationID)
	if err != nil {
		return peer.PairingRequest{}, err
	}
	if found && !peer.SameOutcome(known, o) {
		return peer.PairingRequest{}, peer.ErrOutcomeConflict
	}
	if x.State == peer.ExchangeRefused {
		if err := peer.RefusalError(x.Reason); err != nil {
			return peer.PairingRequest{}, err
		}
	}
	return currentRequest(ctx, tx, o)
}

// commitEpoch commits an epoch-capable outcome: its ledger row, then, for
// an address move, only the immutable outcome; for a candidate the outcome
// and the request's display mirror.
func commitEpoch(ctx context.Context, tx *sql.Tx, o peer.PairingOutcome) (peer.PairingRequest, error) {
	x, err := commitExchange(ctx, tx, outcomeExchange(o))
	if err != nil {
		return peer.PairingRequest{}, err
	}
	if err := insertOutcome(ctx, tx, o); err != nil {
		return peer.PairingRequest{}, err
	}
	if x.Kind == peer.KindAddressMove {
		return currentRequest(ctx, tx, o)
	}
	if err := mirrorCandidate(ctx, tx, o, x.Kind); err != nil {
		return peer.PairingRequest{}, err
	}
	return pairingRequest(ctx, tx, o.IdentityKey)
}

// mirrorCandidate names the candidate on the identity's request, display
// only: a fresh pairing awaits the operator's decision again; a repair
// leaves the request's decision and delivery as they are.
func mirrorCandidate(ctx context.Context, tx *sql.Tx, o peer.PairingOutcome, kind peer.ExchangeKind) error {
	if kind == peer.KindRepair {
		return mirrorRepair(ctx, tx, o)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO peer_pairing_requests (identity_key, name, state, first_paired_at, operation_id)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(identity_key) DO UPDATE SET name = excluded.name, state = excluded.state, delivered_at = NULL, operation_id = excluded.operation_id`,
		o.IdentityKey, o.Name, string(peer.PairingPending), stamp(o.At), o.OperationID)
	if err != nil {
		return fmt.Errorf("store pairing request: %w", err)
	}
	return nil
}

// mirrorRepair names a repair on the identity's request, keeping its
// decision; a pairing this side joined has no request yet, so the repair's
// is a new one, pending like any request.
func mirrorRepair(ctx context.Context, tx *sql.Tx, o peer.PairingOutcome) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO peer_pairing_requests (identity_key, name, state, first_paired_at, operation_id)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(identity_key) DO UPDATE SET operation_id = excluded.operation_id`,
		o.IdentityKey, o.Name, string(peer.PairingPending), stamp(o.At), o.OperationID)
	if err != nil {
		return fmt.Errorf("store repair request: %w", err)
	}
	return nil
}

// currentRequest is the identity's request as it is, or, for a contact
// made by joining that has none, what the outcome names.
func currentRequest(ctx context.Context, tx *sql.Tx, o peer.PairingOutcome) (peer.PairingRequest, error) {
	request, err := pairingRequest(ctx, tx, o.IdentityKey)
	if errors.Is(err, peer.ErrNoPairingRequest) {
		return peer.PairingRequest{IdentityKey: o.IdentityKey, Name: o.Name, Kind: peer.KindAddressMove}, nil
	}
	return request, err
}

// admitPairing refuses a reused invitation, an invalid new outcome or a full
// store, reading the counts in the commit's transaction.
func admitPairing(ctx context.Context, tx *sql.Tx, o peer.PairingOutcome) error {
	if err := invitationUnused(ctx, tx, o.InvitationID); err != nil {
		return err
	}
	if err := peer.ValidNewOutcome(o); err != nil {
		return err
	}
	counts, err := pairingCounts(ctx, tx, o.IdentityKey)
	if err != nil {
		return err
	}
	return peer.AdmitOutcome(counts)
}

// insertPairing writes the request, keeping any decision, and the outcome.
func insertPairing(ctx context.Context, tx *sql.Tx, o peer.PairingOutcome) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_pairing_requests (identity_key, name, state, first_paired_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(identity_key) DO UPDATE SET name = excluded.name`,
		o.IdentityKey, o.Name, string(peer.PairingPending), stamp(o.At)); err != nil {
		return fmt.Errorf("store pairing request: %w", err)
	}
	return insertOutcome(ctx, tx, o)
}

// insertOutcome writes the immutable outcome.
func insertOutcome(ctx context.Context, tx *sql.Tx, o peer.PairingOutcome) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_pairing_outcomes (operation_id, invitation_id, identity_key, name, transcript_id, at)
VALUES (?, ?, ?, ?, ?, ?)`,
		o.OperationID, o.InvitationID, o.IdentityKey, o.Name, o.TranscriptID, stamp(o.At)); err != nil {
		return fmt.Errorf("store pairing outcome: %w", err)
	}
	return nil
}

// invitationUnused refuses a second outcome for one invitation.
func invitationUnused(ctx context.Context, tx *sql.Tx, invitationID string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_pairing_outcomes WHERE invitation_id = ?`, invitationID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return peer.ErrOutcomeConflict
	}
	return nil
}

// pairingCounts reads what admission needs inside the commit's
// transaction: the retained identities are every distinct one a contact, a
// request or a join holds.
func pairingCounts(ctx context.Context, tx *sql.Tx, identityKey string) (peer.PairingCounts, error) {
	var c peer.PairingCounts
	err := tx.QueryRowContext(ctx, `WITH retained(identity_key) AS (
    SELECT identity_key FROM peer_contacts
    UNION SELECT identity_key FROM peer_pairing_requests
    UNION SELECT peer FROM peer_joins WHERE peer <> '')
SELECT
    (SELECT COUNT(*) FROM peer_pairing_outcomes),
    (SELECT COUNT(*) FROM retained),
    (SELECT COUNT(*) FROM peer_pairing_requests WHERE state = ?1),
    EXISTS (SELECT 1 FROM peer_pairing_requests WHERE identity_key = ?2),
    EXISTS (SELECT 1 FROM retained WHERE identity_key = ?2)`,
		string(peer.PairingPending), identityKey).Scan(&c.Outcomes, &c.Identities, &c.Pending, &c.KnownIdentity, &c.Retained)
	return c, err
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// pairingOutcome reads a committed outcome, with the authority inputs its
// ledger row holds, if any.
func pairingOutcome(ctx context.Context, q rowQuerier, operationID string) (peer.PairingOutcome, bool, error) {
	var o peer.PairingOutcome
	var at string
	var mark int64
	err := q.QueryRowContext(ctx, `SELECT o.operation_id, o.invitation_id, o.identity_key, o.name, o.transcript_id, o.at,
    COALESCE(x.mode, ''), COALESCE(x.begun_mark, 0), COALESCE(x.repair_key, '')
FROM peer_pairing_outcomes o LEFT JOIN pairing_exchanges x ON x.operation_id = o.operation_id AND x.role = 'inviter'
WHERE o.operation_id = ?`, operationID).
		Scan(&o.OperationID, &o.InvitationID, &o.IdentityKey, &o.Name, &o.TranscriptID, &at, &o.Mode, &mark, &o.Repair)
	if errors.Is(err, sql.ErrNoRows) {
		return peer.PairingOutcome{}, false, nil
	}
	if err != nil {
		return peer.PairingOutcome{}, false, err
	}
	o.BegunMark, o.TranscriptID = uint64(mark), bytes.Clone(o.TranscriptID)
	if o.Mode == peer.ModeLegacy {
		o.Mode = ""
	}
	o.At, err = time.Parse(time.RFC3339Nano, at)
	return o, err == nil, err
}

// requestSelect reads requests with their candidate's kind.
const requestSelect = `SELECT r.identity_key, r.name, r.state, r.first_paired_at, COALESCE(r.delivered_at, ''), r.operation_id,
    COALESCE(x.kind, '') FROM peer_pairing_requests r LEFT JOIN pairing_exchanges x ON r.operation_id != '' AND x.operation_id = r.operation_id`

func pairingRequest(ctx context.Context, q rowQuerier, identityKey string) (peer.PairingRequest, error) {
	request, found, err := scanPairingRequest(q.QueryRowContext(ctx, requestSelect+` WHERE r.identity_key = ?`, identityKey))
	if err == nil && !found {
		err = peer.ErrNoPairingRequest
	}
	return request, err
}

type rowScanner interface{ Scan(dest ...any) error }

func scanPairingRequest(row rowScanner) (peer.PairingRequest, bool, error) {
	var request peer.PairingRequest
	var state, first, delivered string
	err := row.Scan(&request.IdentityKey, &request.Name, &state, &first, &delivered, &request.OperationID, &request.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return peer.PairingRequest{}, false, nil
	}
	if err != nil {
		return peer.PairingRequest{}, false, err
	}
	request.State = peer.PairingState(state)
	if request.DeliveredAt, err = optionalTime(delivered); err != nil {
		return peer.PairingRequest{}, false, err
	}
	request.FirstPairedAt, err = time.Parse(time.RFC3339Nano, first)
	return request, err == nil, err
}

// optionalTime reads a timestamp column that is empty until set.
func optionalTime(text string) (time.Time, error) {
	if text == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, text)
}

// optionalStamp writes a timestamp, or NULL while it is unset.
func optionalStamp(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return stamp(t)
}

// Deliver records the joiner's acknowledgement of a legacy request's
// decision; see peer.PairingStore. An accepted one becomes a contact in the
// same transaction. An identity with an epoch-capable exchange is
// peer.ErrOperationRequired and nothing is written.
func (s *Store) Deliver(ctx context.Context, identityKey string, decision peer.PairingState, at time.Time) (peer.PairingRequest, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return peer.PairingRequest{}, err
	}
	defer tx.Rollback()
	if err := legacyDelivery(ctx, tx, identityKey); err != nil {
		return peer.PairingRequest{}, err
	}
	request, err := pairingRequest(ctx, tx, identityKey)
	if err != nil {
		return peer.PairingRequest{}, err
	}
	next, changed, err := peer.DeliverDecision(request, decision, at)
	if err != nil || !changed {
		return next, err
	}
	if err := deliver(ctx, tx, next); err != nil {
		return request, err
	}
	return next, tx.Commit()
}

func deliver(ctx context.Context, tx *sql.Tx, r peer.PairingRequest) error {
	if _, err := tx.ExecContext(ctx, `UPDATE peer_pairing_requests SET delivered_at = ? WHERE identity_key = ?`,
		stamp(r.DeliveredAt), r.IdentityKey); err != nil {
		return err
	}
	if r.State != peer.PairingAccepted {
		return nil
	}
	return upsertContact(ctx, tx, peer.InviterContact(r), "") // it carries no destination, so never moves
}

// legacyDelivery refuses an identity-only delivery to an identity with an
// epoch-capable exchange: its delivery must name the operation.
func legacyDelivery(ctx context.Context, tx *sql.Tx, identityKey string) error {
	epoch, err := epochIdentity(ctx, tx, identityKey)
	if err == nil && epoch {
		err = peer.ErrOperationRequired
	}
	return err
}

// Outcome looks up a committed operation.
func (s *Store) Outcome(ctx context.Context, operationID string) (peer.PairingOutcome, bool, error) {
	return pairingOutcome(ctx, s.db, operationID)
}

// decidePairing applies the operator's decision: to the open epoch-capable
// candidate the request names, if any, else to the legacy request.
func (s *Store) decidePairing(ctx context.Context, identityKey string, decision peer.PairingState) (peer.PairingRequest, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return peer.PairingRequest{}, err
	}
	defer tx.Rollback()
	request, err := pairingRequest(ctx, tx, identityKey)
	if err != nil {
		return peer.PairingRequest{}, err
	}
	request, err = decideRequest(ctx, tx, request, decision)
	if err != nil {
		return request, err
	}
	if err := tx.Commit(); err != nil {
		return peer.PairingRequest{}, err
	}
	return request, nil
}

// decideRequest decides the request's open candidate on its ledger row,
// if there is one: a fresh pairing's request mirrors the decision, a
// repair's keeps its own unless the repair made the request, still
// pending. Otherwise it decides the legacy request.
func decideRequest(ctx context.Context, tx *sql.Tx, request peer.PairingRequest, decision peer.PairingState) (peer.PairingRequest, error) {
	x, found, err := exchangeRow(ctx, tx, request.OperationID)
	if err != nil {
		return request, err
	}
	if found && x.State == peer.ExchangeOpen {
		if err := decideCandidate(ctx, tx, x, decision); err != nil || keepsDecision(x, request) {
			return request, err
		}
	}
	return setRequestState(ctx, tx, request, decision)
}

// keepsDecision: a repair leaves a decided request as it is.
func keepsDecision(x peer.ExchangeRecord, request peer.PairingRequest) bool {
	return x.Kind == peer.KindRepair && request.State != peer.PairingPending
}

// decideCandidate records the operator's decision on the candidate's row.
func decideCandidate(ctx context.Context, tx *sql.Tx, x peer.ExchangeRecord, decision peer.PairingState) error {
	if _, err := peer.DecidePairing(x.Decision, decision); err != nil {
		return err
	}
	return decideExchange(ctx, tx, exchangeDecision{x.OperationID, decision, time.Now()})
}

// setRequestState applies a decision to a request's state.
func setRequestState(ctx context.Context, tx *sql.Tx, request peer.PairingRequest, decision peer.PairingState) (peer.PairingRequest, error) {
	state, err := peer.DecidePairing(request.State, decision)
	if err != nil {
		return request, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peer_pairing_requests SET state = ? WHERE identity_key = ?`, string(state), request.IdentityKey); err != nil {
		return peer.PairingRequest{}, err
	}
	request.State = state
	return request, nil
}

// Accept decides a pending request in favour; see peer.PairingStore.
func (s *Store) Accept(ctx context.Context, identityKey string) (peer.PairingRequest, error) {
	return s.decidePairing(ctx, identityKey, peer.PairingAccepted)
}

// Reject decides a pending request against; see peer.PairingStore.
func (s *Store) Reject(ctx context.Context, identityKey string) (peer.PairingRequest, error) {
	return s.decidePairing(ctx, identityKey, peer.PairingRejected)
}

// Request is the identity's pairing request, if any.
func (s *Store) Request(ctx context.Context, identityKey string) (peer.PairingRequest, bool, error) {
	return scanPairingRequest(s.db.QueryRowContext(ctx, requestSelect+` WHERE r.identity_key = ?`, identityKey))
}

// Requests lists every pairing request by identity key.
func (s *Store) Requests(ctx context.Context) ([]peer.PairingRequest, error) {
	rows, err := s.db.QueryContext(ctx, requestSelect+` ORDER BY r.identity_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []peer.PairingRequest
	for rows.Next() {
		request, _, err := scanPairingRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, request)
	}
	return out, rows.Err()
}
