package store

import (
	"context"
	"time"
)

// The agents this workspace lists in each Pairbook v1 space. Listing
// advertises an agent to the space's members; who may message it is still
// this workspace's contact sharing.

const spaceSharesSchema = `
CREATE TABLE IF NOT EXISTS space_shares (
    space    TEXT NOT NULL,
    agent    TEXT NOT NULL,
    added_at TEXT NOT NULL,
    PRIMARY KEY (space, agent)
);
`

// ShareInSpace lists agent in space.
func (s *Store) ShareInSpace(ctx context.Context, space, agent string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO space_shares (space, agent, added_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, space, agent, msgStamp(at))
	return err
}

// UnshareInSpace stops listing agent in space.
func (s *Store) UnshareInSpace(ctx context.Context, space, agent string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM space_shares WHERE space = ? AND agent = ?`, space, agent)
	return err
}

// SpaceShares are the agents listed in space, by name.
func (s *Store) SpaceShares(ctx context.Context, space string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent FROM space_shares WHERE space = ? ORDER BY agent`, space)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
