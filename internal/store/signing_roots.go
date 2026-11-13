package store

import (
	"context"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Signing roots (#115): the keys this workspace verifies seals against.
// The operator's person keys are pinned when the operator registers one
// over the authenticated operator socket, which is the operator's approval.
// Keys are only ever added: an older key keeps its messages verifiable.

const signingRootsSchema = `
CREATE TABLE IF NOT EXISTS signing_roots (
    key_id     TEXT PRIMARY KEY,
    person     INTEGER NOT NULL,
    workspace  TEXT NOT NULL,
    approved   INTEGER NOT NULL,
    created_at TEXT NOT NULL
);
`

// ErrRootConflict: the key is pinned already, as another kind of root or
// for another workspace; a key is bound once.
var ErrRootConflict = errors.New("store: signing key pinned with another binding")

// PinSigningRoot pins root, or updates whether a pinned one is approved; a
// key pinned with another binding is refused, never rebound.
func (s *Store) PinSigningRoot(ctx context.Context, root protocol.SigningRoot, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO signing_roots (key_id, person, workspace, approved, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(key_id) DO UPDATE SET approved = excluded.approved
		WHERE signing_roots.person = excluded.person AND signing_roots.workspace = excluded.workspace`,
		root.KeyID, root.Person, root.Workspace, root.Approved, stamp(at))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errors.Join(ErrRootConflict, err)
	}
	return nil
}

// SigningRoots are the pinned roots, oldest first.
func (s *Store) SigningRoots(ctx context.Context) ([]protocol.SigningRoot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key_id, person, workspace, approved FROM signing_roots ORDER BY created_at, key_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roots []protocol.SigningRoot
	for rows.Next() {
		var r protocol.SigningRoot
		if err := rows.Scan(&r.KeyID, &r.Person, &r.Workspace, &r.Approved); err != nil {
			return nil, err
		}
		roots = append(roots, r)
	}
	return roots, rows.Err()
}
