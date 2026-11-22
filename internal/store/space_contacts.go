package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// Space-managed contacts (Pairbook v1). A contact a space connect
// installed is managed: the reconciler may retire it when no space grants
// it any more. The mark records the pairing epoch it installed, written
// in the install's own transaction, so a retire never ends a newer pairing
// (a manual one, say). A refusal is the operator's own pause or unpair of
// a workspace: contact-wide, in every space and both directions, kept
// across restarts, until the operator resumes it or pairs it by code.

const spaceContactsSchema = `
CREATE TABLE IF NOT EXISTS space_contacts (
    workspace    TEXT PRIMARY KEY,
    epoch        INTEGER NOT NULL,
    installed_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS space_refusals (
    workspace TEXT PRIMARY KEY,
    at        TEXT NOT NULL
);
`

// ErrSpaceRefused: the operator refused this workspace (paused or
// unpaired it), so no space connects it.
var ErrSpaceRefused = errors.New("store: the operator refused this workspace")

// SpaceRefused: the operator refused workspace.
func (s *Store) SpaceRefused(ctx context.Context, workspace string) (bool, error) {
	return spaceRefused(ctx, s.db, workspace)
}

func spaceRefused(ctx context.Context, q rowQuerier, workspace string) (bool, error) {
	var refused bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM space_refusals WHERE workspace = ?)`, workspace).Scan(&refused)
	return refused, err
}

// refuseSpace records the operator's refusal of workspace.
func refuseSpace(ctx context.Context, tx *sql.Tx, workspace string, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO space_refusals (workspace, at) VALUES (?, ?) ON CONFLICT DO NOTHING`, workspace, msgStamp(at))
	return err
}

// allowSpace lifts the operator's refusal of workspace.
func allowSpace(ctx context.Context, tx *sql.Tx, workspace string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM space_refusals WHERE workspace = ?`, workspace)
	return err
}

// markManaged records, in the install's transaction, that the contact's
// current pairing is space-managed.
func markManaged(ctx context.Context, tx *sql.Tx, workspace string, at time.Time) error {
	c, err := readPairing(ctx, tx, workspace)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO space_contacts (workspace, epoch, installed_at) VALUES (?1, ?2, ?3)
ON CONFLICT (workspace) DO UPDATE SET epoch = ?2, installed_at = ?3`, workspace, c.epoch, msgStamp(at))
	return err
}

// ManagedSpaceContacts are the space-managed contacts and the pairing epoch
// each was installed at.
func (s *Store) ManagedSpaceContacts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workspace, epoch FROM space_contacts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var w string
		var epoch int64
		if err := rows.Scan(&w, &epoch); err != nil {
			return nil, err
		}
		out[w] = epoch
	}
	return out, rows.Err()
}

// RetireSpaceContact ends the space-managed pairing of workspace at epoch,
// in one transaction: only if it is still managed at that epoch and still
// that contact's current, live pairing. A newer pairing is left alone and
// only the stale mark goes. The peer is told, as for any unpair; no
// refusal is recorded, so a later grant connects again.
func (s *Store) RetireSpaceContact(ctx context.Context, workspace string, epoch int64, at time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	retired, err := retireManaged(ctx, tx, workspace, epoch, at)
	if err != nil {
		return false, err
	}
	return retired, tx.Commit()
}

func retireManaged(ctx context.Context, tx *sql.Tx, workspace string, epoch int64, at time.Time) (bool, error) {
	var marked int64
	err := tx.QueryRowContext(ctx, `SELECT epoch FROM space_contacts WHERE workspace = ?`, workspace).Scan(&marked)
	if errors.Is(err, sql.ErrNoRows) || err == nil && marked != epoch {
		return false, err // not managed, or managed at another epoch: not this retire's
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM space_contacts WHERE workspace = ?`, workspace); err != nil {
		return false, err
	}
	c, err := readPairing(ctx, tx, workspace)
	if err != nil || c.epoch != epoch || c.unpairedAt != "" {
		return false, err // a newer pairing, or already ended
	}
	_, err = unpairNow(ctx, tx, ContactUnpair{Contact: workspace, At: at}, &c)
	return err == nil, err
}

// installSpaceManaged is installSpace for a refused workspace's refusal
// and the managed mark, in the same transaction.
func installSpaceManaged(ctx context.Context, tx *sql.Tx, in peer.SpaceInstall, at time.Time) (peer.InstallResult, error) {
	refused, err := spaceRefused(ctx, tx, in.Contact.IdentityKey)
	if err != nil {
		return peer.InstallResult{}, err
	}
	if refused {
		return peer.InstallResult{}, ErrSpaceRefused
	}
	result, err := installSpace(ctx, tx, in, at)
	if err != nil || result.Exchange.State != peer.ExchangeInstalled {
		return result, err
	}
	return result, markManaged(ctx, tx, in.Contact.IdentityKey, at)
}
