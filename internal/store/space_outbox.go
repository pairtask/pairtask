package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// The space outbox (Pairbook v1) keeps each signed map, invite or change
// before it is sent to the directory, so a retry after a lost reply sends
// the very same bytes, never a re-signed record with a new time or digest.
// A join's invite secret is kept with its change only until the send
// settles, then erased with the row. The invite ledger is the owner's
// record of the invites it issued, by space and owner key, so it can tell
// a live invite from one it never issued or already gave to a join.

const spaceOutboxSchema = `
CREATE TABLE IF NOT EXISTS space_outbox (
    kind       TEXT NOT NULL,
    digest     TEXT NOT NULL,
    space      TEXT NOT NULL,
    base       TEXT NOT NULL DEFAULT '',
    owner      TEXT NOT NULL DEFAULT '',
    body       BLOB NOT NULL,
    token      BLOB,
    expires_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY (kind, digest)
);
CREATE TABLE IF NOT EXISTS space_invites (
    space        TEXT NOT NULL,
    owner        TEXT NOT NULL,
    token_hash   TEXT NOT NULL,
    base_version TEXT NOT NULL,
    base_digest  TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    consumed_by  TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (space, owner, token_hash)
);
`

// SpaceSend is one record waiting to reach a space's directory: its kind
// (map, invite or change), digest, space, the directory base for a space
// not pinned yet, the signed body, and a join's secret.
type SpaceSend struct {
	Kind, Digest, Space, Base string
	Owner                     string // the owner key ID pinned when queued
	Body                      []byte
	Token                     []byte
	ExpiresAt                 time.Time // zero: kept until settled; a join's secret goes at its deadline
	CreatedAt                 time.Time
}

// QueueSpaceSend keeps item unless the same record is already queued, and
// returns what is kept: a retry always gets the first bytes.
func (s *Store) QueueSpaceSend(ctx context.Context, item SpaceSend) (SpaceSend, error) {
	if !item.complete() {
		return SpaceSend{}, errors.New("store: incomplete space send")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO space_outbox (kind, digest, space, base, owner, body, token, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING`, item.Kind, item.Digest, item.Space, item.Base, item.Owner, item.Body, item.Token, sendStamp(item.ExpiresAt), msgStamp(item.CreatedAt)); err != nil {
		return SpaceSend{}, err
	}
	rows, err := s.spaceSends(ctx, `WHERE kind = ? AND digest = ?`, item.Kind, item.Digest)
	if err != nil || len(rows) != 1 {
		return SpaceSend{}, errors.Join(err, errors.New("store: space send not kept"))
	}
	return rows[0], nil
}

// complete: the record names what it is and carries a body.
func (it SpaceSend) complete() bool {
	named := it.Kind != "" && it.Digest != ""
	return named && len(it.Body) > 0
}

// SpaceSends are the records still waiting, oldest first.
func (s *Store) SpaceSends(ctx context.Context) ([]SpaceSend, error) {
	return s.spaceSends(ctx, ``)
}

func (s *Store) spaceSends(ctx context.Context, where string, args ...any) ([]SpaceSend, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, digest, space, base, owner, body, token, expires_at, created_at FROM space_outbox `+where+` ORDER BY created_at, kind, digest`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpaceSend
	for rows.Next() {
		var it SpaceSend
		var expires, at string
		if err := rows.Scan(&it.Kind, &it.Digest, &it.Space, &it.Base, &it.Owner, &it.Body, &it.Token, &expires, &at); err != nil {
			return nil, err
		}
		if it.CreatedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		if it.ExpiresAt, err = parseSendStamp(expires); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SettleSpaceSend forgets a record the directory answered for good,
// secret included. Settling one already gone is no error.
func (s *Store) SettleSpaceSend(ctx context.Context, kind, digest string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM space_outbox WHERE kind = ? AND digest = ?`, kind, digest)
	return err
}

func sendStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return msgStamp(t)
}

func parseSendStamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// SpaceInvite is an invite this workspace issued as a space's owner.
type SpaceInvite struct {
	Space, Owner, TokenHash string
	BaseVersion             uint64
	BaseDigest              string
	ExpiresAt               time.Time
}

// KeepSpaceInvite records an issued invite; the same one again is a no-op.
func (s *Store) KeepSpaceInvite(ctx context.Context, inv SpaceInvite) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO space_invites (space, owner, token_hash, base_version, base_digest, expires_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING`, inv.Space, inv.Owner, inv.TokenHash, strconv.FormatUint(inv.BaseVersion, 10), inv.BaseDigest, msgStamp(inv.ExpiresAt))
	return err
}

// DropSpaceInvite forgets an invite the directory refused, so it never
// counts as live.
func (s *Store) DropSpaceInvite(ctx context.Context, key SpaceInvite) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM space_invites WHERE space = ? AND owner = ? AND token_hash = ? AND consumed_by = ''`,
		key.Space, key.Owner, key.TokenHash)
	return err
}

// InviteState is what the ledger knows of a token hash for one change.
type InviteState int

const (
	InviteUnknown InviteState = iota // not issued here: the join stays pending
	InviteLive                       // issued here, valid, free or held by this change
	InviteSpent                      // expired, or given to another change
)

// SpaceInviteFor is the state of space's invite tokenHash, issued under
// owner, for the change digest at now.
func (s *Store) SpaceInviteFor(ctx context.Context, key SpaceInvite, change string, now time.Time) (InviteState, error) {
	var expires string
	var row inviteRow
	err := s.db.QueryRowContext(ctx, `SELECT expires_at, consumed_by FROM space_invites WHERE space = ? AND owner = ? AND token_hash = ?`,
		key.Space, key.Owner, key.TokenHash).Scan(&expires, &row.consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return InviteUnknown, nil
	}
	if err != nil {
		return InviteUnknown, err
	}
	if row.expires, err = time.Parse(time.RFC3339Nano, expires); err != nil {
		return InviteUnknown, err
	}
	return row.state(change, now), nil
}

// inviteRow is an issued invite as the ledger holds it.
type inviteRow struct {
	expires  time.Time
	consumed string
}

// state is the invite's state for change at now.
func (r inviteRow) state(change string, now time.Time) InviteState {
	if !now.Before(r.expires) || r.takenByAnother(change) {
		return InviteSpent
	}
	return InviteLive
}

// takenByAnother: the invite went to a change other than change.
func (r inviteRow) takenByAnother(change string) bool {
	return r.consumed != "" && r.consumed != change
}

// ConsumeSpaceInvite gives the invite to change, once the map that admits
// it is kept; another change already holding it is an error.
func (s *Store) ConsumeSpaceInvite(ctx context.Context, key SpaceInvite, change string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE space_invites SET consumed_by = ? WHERE space = ? AND owner = ? AND token_hash = ? AND consumed_by IN ('', ?)`,
		change, key.Space, key.Owner, key.TokenHash, change)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, errors.New("store: invite not live for this change"))
	}
	return nil
}
