package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// The addresses this workspace publishes in each Pairbook v1 space, kept so
// the broker renews the record before it lapses, and when it last did.

const spaceAddressesSchema = `
CREATE TABLE IF NOT EXISTS space_addresses (
    space        TEXT PRIMARY KEY,
    addrs        TEXT NOT NULL,
    valid_ms     INTEGER NOT NULL,
    published_at TEXT NOT NULL DEFAULT ''
);
`

// SpaceAddress is what this workspace publishes in a space.
type SpaceAddress struct {
	Space       string
	Addrs       []string
	Valid       time.Duration
	PublishedAt time.Time // zero: not published yet
}

// KeepSpaceAddress records the addresses to publish in a space.
func (s *Store) KeepSpaceAddress(ctx context.Context, a SpaceAddress) error {
	addrs, err := json.Marshal(a.Addrs)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO space_addresses (space, addrs, valid_ms) VALUES (?1, ?2, ?3)
ON CONFLICT (space) DO UPDATE SET addrs = ?2, valid_ms = ?3, published_at = ''`, a.Space, string(addrs), a.Valid.Milliseconds())
	return err
}

// ForgetSpaceAddress drops the address setting of space: nothing renews it.
func (s *Store) ForgetSpaceAddress(ctx context.Context, space string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM space_addresses WHERE space = ?`, space)
	return err
}

// PublishedSpaceAddress records when the address of space was published.
func (s *Store) PublishedSpaceAddress(ctx context.Context, space string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE space_addresses SET published_at = ? WHERE space = ?`, msgStamp(at), space)
	return err
}

// SpaceAddressOf is the address setting kept for space.
func (s *Store) SpaceAddressOf(ctx context.Context, space string) (SpaceAddress, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT space, addrs, valid_ms, published_at FROM space_addresses WHERE space = ?`, space)
	a, err := scanSpaceAddress(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	return a, err == nil, err
}

// SpaceAddresses are the addresses kept for every space.
func (s *Store) SpaceAddresses(ctx context.Context) ([]SpaceAddress, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT space, addrs, valid_ms, published_at FROM space_addresses ORDER BY space`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpaceAddress
	for rows.Next() {
		a, err := scanSpaceAddress(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanSpaceAddress(scan func(...any) error) (SpaceAddress, error) {
	var a SpaceAddress
	var addrs, published string
	var valid int64
	if err := scan(&a.Space, &addrs, &valid, &published); err != nil {
		return a, err
	}
	if err := json.Unmarshal([]byte(addrs), &a.Addrs); err != nil {
		return a, err
	}
	a.Valid = time.Duration(valid) * time.Millisecond
	var err error
	a.PublishedAt, err = parseSendStamp(published)
	return a, err
}
