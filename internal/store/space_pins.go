package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/space"
)

// spacePinsSchema keeps one pin per Pairbook space this workspace created
// or joined: the owner key and directory it trusts, never replaced.
const spacePinsSchema = `
CREATE TABLE IF NOT EXISTS space_pins (
    space     TEXT PRIMARY KEY,
    owner     TEXT NOT NULL,
    base      TEXT NOT NULL,
    joined_at TEXT NOT NULL
);
`

// PinSpace keeps p. The same pin again is a no-op that keeps the first
// JoinedAt; another owner or directory for a pinned space is
// space.ErrPinConflict and changes nothing.
func (s *Store) PinSpace(ctx context.Context, p space.Pin) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO space_pins (space, owner, base, joined_at) VALUES (?, ?, ?, ?) ON CONFLICT (space) DO NOTHING`,
		p.Space, p.Owner, p.Base, p.JoinedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	held, _, err := spacePin(ctx, tx, p.Space)
	if err != nil {
		return err
	}
	if held.Owner != p.Owner || held.Base != p.Base {
		return space.ErrPinConflict
	}
	return tx.Commit()
}

// SpacePin is the pin for id; false when there is none.
func (s *Store) SpacePin(ctx context.Context, id string) (space.Pin, bool, error) {
	return spacePin(ctx, s.db, id)
}

// SpacePins is every pin, by space.
func (s *Store) SpacePins(ctx context.Context) ([]space.Pin, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT space, owner, base, joined_at FROM space_pins ORDER BY space`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pins []space.Pin
	for rows.Next() {
		p, err := scanPin(rows)
		if err != nil {
			return nil, err
		}
		pins = append(pins, p)
	}
	return pins, rows.Err()
}

// UnpinSpace forgets id's pin, after leaving or deleting the space; an
// unknown space is already forgotten.
func (s *Store) UnpinSpace(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"space_maps", "space_addresses", "space_shares"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE space = ?`, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM space_pins WHERE space = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func spacePin(ctx context.Context, q rowQuerier, id string) (space.Pin, bool, error) {
	p, err := scanPin(q.QueryRowContext(ctx, `SELECT space, owner, base, joined_at FROM space_pins WHERE space = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return space.Pin{}, false, nil
	}
	return p, err == nil, err
}

func scanPin(row interface{ Scan(...any) error }) (space.Pin, error) {
	var p space.Pin
	var joined string
	if err := row.Scan(&p.Space, &p.Owner, &p.Base, &joined); err != nil {
		return p, err
	}
	at, err := time.Parse(time.RFC3339Nano, joined)
	p.JoinedAt = at
	return p, err
}
