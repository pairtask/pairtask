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
// per successful pairing operation (#178). The identity key is provisional,
// see peer.PairingRequest; names are metadata only.
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

func pairingTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Commit writes the outcome and its request in one transaction; see
// peer.PairingStore.
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
	if err != nil {
		return peer.PairingRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return peer.PairingRequest{}, fmt.Errorf("commit pairing: %w", err)
	}
	return request, nil
}

func commitPairing(ctx context.Context, tx *sql.Tx, o peer.PairingOutcome) (peer.PairingRequest, error) {
	known, found, err := pairingOutcome(ctx, tx, o.OperationID)
	if err != nil {
		return peer.PairingRequest{}, err
	}
	if found {
		if !peer.SameOutcome(known, o) {
			return peer.PairingRequest{}, peer.ErrOutcomeConflict
		}
		return pairingRequest(ctx, tx, o.IdentityKey)
	}
	if err := invitationUnused(ctx, tx, o.InvitationID); err != nil {
		return peer.PairingRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_pairing_requests (identity_key, name, state, first_paired_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(identity_key) DO UPDATE SET name = excluded.name`,
		o.IdentityKey, o.Name, string(peer.PairingPending), pairingTime(o.At)); err != nil {
		return peer.PairingRequest{}, fmt.Errorf("store pairing request: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_pairing_outcomes (operation_id, invitation_id, identity_key, name, transcript_id, at)
VALUES (?, ?, ?, ?, ?, ?)`,
		o.OperationID, o.InvitationID, o.IdentityKey, o.Name, o.TranscriptID, pairingTime(o.At)); err != nil {
		return peer.PairingRequest{}, fmt.Errorf("store pairing outcome: %w", err)
	}
	return pairingRequest(ctx, tx, o.IdentityKey)
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

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func pairingOutcome(ctx context.Context, q rowQuerier, operationID string) (peer.PairingOutcome, bool, error) {
	var o peer.PairingOutcome
	var at string
	err := q.QueryRowContext(ctx, `SELECT operation_id, invitation_id, identity_key, name, transcript_id, at
FROM peer_pairing_outcomes WHERE operation_id = ?`, operationID).
		Scan(&o.OperationID, &o.InvitationID, &o.IdentityKey, &o.Name, &o.TranscriptID, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return peer.PairingOutcome{}, false, nil
	}
	if err != nil {
		return peer.PairingOutcome{}, false, err
	}
	o.TranscriptID = bytes.Clone(o.TranscriptID)
	o.At, err = time.Parse(time.RFC3339Nano, at)
	return o, err == nil, err
}

func pairingRequest(ctx context.Context, q rowQuerier, identityKey string) (peer.PairingRequest, error) {
	request, found, err := scanPairingRequest(q.QueryRowContext(ctx,
		`SELECT identity_key, name, state, first_paired_at FROM peer_pairing_requests WHERE identity_key = ?`, identityKey))
	if err == nil && !found {
		err = peer.ErrNoPairingRequest
	}
	return request, err
}

type rowScanner interface{ Scan(dest ...any) error }

func scanPairingRequest(row rowScanner) (peer.PairingRequest, bool, error) {
	var request peer.PairingRequest
	var state, first string
	err := row.Scan(&request.IdentityKey, &request.Name, &state, &first)
	if errors.Is(err, sql.ErrNoRows) {
		return peer.PairingRequest{}, false, nil
	}
	if err != nil {
		return peer.PairingRequest{}, false, err
	}
	request.State = peer.PairingState(state)
	request.FirstPairedAt, err = time.Parse(time.RFC3339Nano, first)
	return request, err == nil, err
}

// Outcome looks up a committed operation.
func (s *Store) Outcome(ctx context.Context, operationID string) (peer.PairingOutcome, bool, error) {
	return pairingOutcome(ctx, s.db, operationID)
}

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
	state, err := peer.DecidePairing(request.State, decision)
	if err != nil {
		return request, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peer_pairing_requests SET state = ? WHERE identity_key = ?`, string(state), identityKey); err != nil {
		return peer.PairingRequest{}, err
	}
	if err := tx.Commit(); err != nil {
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
	return scanPairingRequest(s.db.QueryRowContext(ctx,
		`SELECT identity_key, name, state, first_paired_at FROM peer_pairing_requests WHERE identity_key = ?`, identityKey))
}

// Requests lists every pairing request by identity key.
func (s *Store) Requests(ctx context.Context) ([]peer.PairingRequest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT identity_key, name, state, first_paired_at FROM peer_pairing_requests ORDER BY identity_key`)
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
