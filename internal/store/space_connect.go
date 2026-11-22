package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// spaceAttemptsSchema keeps the connect ID of each space connect this
// workspace began and hasn't finished, so a retry after a restart repeats
// the same attempt and converges on the same pairing.
const spaceAttemptsSchema = `
CREATE TABLE IF NOT EXISTS space_attempts (
    target     TEXT PRIMARY KEY,
    space      TEXT NOT NULL,
    connect_id BLOB NOT NULL
);
`

// spaceOperation is a space connect's exchange operation ID: the space
// and the attempt, so one attempt can't stand for another space's.
func spaceOperation(in peer.SpaceInstall) string {
	return peer.SpaceOperationPrefix + in.Space + ":" + hex.EncodeToString(in.ConnectID[:])
}

// InstallSpaceContact installs in as an epoch pairing (kind pair, mode
// epoch), through the same path a code exchange's install takes. The same
// space, connect ID, peer, pairing ID and role again is a no-op; any other
// use of that operation, or a contact already paired with the peer, is
// peer.ErrSpaceConflict and changes nothing.
func (s *Store) InstallSpaceContact(ctx context.Context, in peer.SpaceInstall) (peer.InstallResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return peer.InstallResult{}, err
	}
	defer tx.Rollback()
	result, err := installSpaceManaged(ctx, tx, in, time.Now())
	if err != nil {
		return peer.InstallResult{}, err
	}
	return result, tx.Commit()
}

// installSpace is InstallSpaceContact inside its transaction: a known
// operation answers its ledger row; a new one installs only for a peer
// with no active pairing.
func installSpace(ctx context.Context, tx *sql.Tx, in peer.SpaceInstall, at time.Time) (peer.InstallResult, error) {
	x, found, err := exchangeRow(ctx, tx, spaceOperation(in))
	if err != nil {
		return peer.InstallResult{}, err
	}
	if found {
		return replaySpace(x, in)
	}
	c, err := readAuthority(ctx, tx, in.Contact.IdentityKey)
	if err != nil {
		return peer.InstallResult{}, err
	}
	if c.active() {
		return peer.InstallResult{}, peer.ErrSpaceConflict
	}
	x, err = recordSpace(ctx, tx, in, at)
	if err != nil {
		return peer.InstallResult{}, err
	}
	return install(ctx, tx, installing{x: x, decision: peer.PairingAccepted, at: at, contact: in.Contact}, c)
}

// replaySpace answers an operation already in the ledger: the same tuple
// gets what its row installed, or its retirement, and writes nothing.
func replaySpace(x peer.ExchangeRecord, in peer.SpaceInstall) (peer.InstallResult, error) {
	if !sameSpaceInstall(x, in) {
		return peer.InstallResult{}, peer.ErrSpaceConflict
	}
	return historicalResult(x), nil
}

// sameSpaceInstall: the ledger row is in's own: the same peer, role and
// pairing ID under the operation both name.
func sameSpaceInstall(x peer.ExchangeRecord, in peer.SpaceInstall) bool {
	return x.SpacePairingID != nil && x.IdentityKey == in.Contact.IdentityKey && x.Role == in.Role && bytes.Equal(x.SpacePairingID, in.PairingID[:])
}

// recordSpace writes the connect's ledger row, accepted: both members
// already verified each other before either installs.
func recordSpace(ctx context.Context, tx *sql.Tx, in peer.SpaceInstall, at time.Time) (peer.ExchangeRecord, error) {
	if in.Role != "joiner" && in.Role != "inviter" {
		return peer.ExchangeRecord{}, errors.New("store: space install role must be joiner or inviter")
	}
	mark, err := readMark(ctx, tx)
	if err != nil {
		return peer.ExchangeRecord{}, err
	}
	op := spaceOperation(in)
	x := peer.ExchangeRecord{OperationID: op, IdentityKey: in.Contact.IdentityKey, Role: in.Role, Kind: peer.KindPair, Mode: peer.ModeEpoch,
		TranscriptID: in.PairingID[:], BegunMark: uint64(mark), State: peer.ExchangeOpen}
	if err := insertExchange(ctx, tx, x, at); err != nil {
		return peer.ExchangeRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pairing_exchanges SET decision = 'accepted', decided_at = ?, space_pairing_id = ? WHERE operation_id = ?`,
		stamp(at), in.PairingID[:], op); err != nil {
		return peer.ExchangeRecord{}, err
	}
	x, _, err = exchangeRow(ctx, tx, op)
	return x, err
}

// SpaceAttempt is the one attempt this workspace keeps for target: the
// space and connect ID already kept, or space and fresh, kept now. A retry,
// even after a restart, repeats it; the caller ends it first if its space
// no longer grants target.
func (s *Store) SpaceAttempt(ctx context.Context, target, space string, fresh [16]byte) (string, [16]byte, error) {
	var id [16]byte
	if _, err := s.db.ExecContext(ctx, `INSERT INTO space_attempts (target, space, connect_id) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
		target, space, fresh[:]); err != nil {
		return "", id, err
	}
	var kept []byte
	var keptSpace string
	if err := s.db.QueryRowContext(ctx, `SELECT space, connect_id FROM space_attempts WHERE target = ?`, target).Scan(&keptSpace, &kept); err != nil {
		return "", id, err
	}
	if len(kept) != len(id) {
		return "", id, errors.New("store: malformed space attempt")
	}
	copy(id[:], kept)
	return keptSpace, id, nil
}

// EndSpaceAttempt forgets target's attempt; one already ended is no error.
func (s *Store) EndSpaceAttempt(ctx context.Context, target string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM space_attempts WHERE target = ?`, target)
	return err
}
