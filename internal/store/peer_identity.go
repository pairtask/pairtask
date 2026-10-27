package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// peerIdentitySchema records the one workspace identity this database
// initialized (#178), so a lost key can't silently become a new workspace.
const peerIdentitySchema = `
CREATE TABLE IF NOT EXISTS peer_identity (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    fingerprint TEXT NOT NULL,
    created_at  TEXT NOT NULL
);
`

var _ peer.IdentityMarker = (*Store)(nil)

// PeerDir is where this database's workspace key lives: beside the database
// and named after it, so two databases never share a key. An in-memory or
// unnamed database has none.
func (s *Store) PeerDir() string {
	if s.path == "" || s.path == ":memory:" {
		return ""
	}
	return s.path + ".peer"
}

// IdentityFingerprint is the recorded workspace identity, if any.
func (s *Store) IdentityFingerprint(ctx context.Context) (string, bool, error) {
	var fp string
	err := s.db.QueryRowContext(ctx, `SELECT fingerprint FROM peer_identity WHERE id = 1`).Scan(&fp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return fp, err == nil, err
}

// InitIdentity records fp once; see peer.IdentityMarker.
func (s *Store) InitIdentity(ctx context.Context, fp string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_identity (id, fingerprint, created_at) VALUES (1, ?, ?) ON CONFLICT(id) DO NOTHING`,
		fp, stamp(time.Now())); err != nil {
		return err
	}
	var recorded string
	if err := tx.QueryRowContext(ctx, `SELECT fingerprint FROM peer_identity WHERE id = 1`).Scan(&recorded); err != nil {
		return err
	}
	if recorded != fp {
		return peer.ErrIdentityMismatch
	}
	return tx.Commit()
}
