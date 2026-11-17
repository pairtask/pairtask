package store

import (
	"context"
	"errors"

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

var errSpaceNotImplemented = errors.New("store: space pins not implemented")

// PinSpace keeps p. The same pin again is a no-op; another owner or
// directory for a pinned space is space.ErrPinConflict.
func (s *Store) PinSpace(ctx context.Context, p space.Pin) error { return errSpaceNotImplemented }

// SpacePin is the pin for id; false when there is none.
func (s *Store) SpacePin(ctx context.Context, id string) (space.Pin, bool, error) {
	return space.Pin{}, false, errSpaceNotImplemented
}

// SpacePins is every pin, by space.
func (s *Store) SpacePins(ctx context.Context) ([]space.Pin, error) {
	return nil, errSpaceNotImplemented
}

// UnpinSpace forgets id's pin, after leaving or deleting the space.
func (s *Store) UnpinSpace(ctx context.Context, id string) error { return errSpaceNotImplemented }
