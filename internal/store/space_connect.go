package store

import (
	"context"
	"errors"

	"github.com/pairtask/pairtask/internal/peer"
)

var errConnectNotImplemented = errors.New("store: space connect not implemented")

// InstallSpaceContact installs in as an epoch pairing (kind pair, mode
// epoch), through the same path a code exchange's install takes. The same
// space, connect ID, peer, pairing ID and role again is a no-op; any other
// use of that operation, or a confirmed contact already held for the peer,
// is peer.ErrSpaceConflict and changes nothing.
func (s *Store) InstallSpaceContact(ctx context.Context, in peer.SpaceInstall) (peer.InstallResult, error) {
	return peer.InstallResult{}, errConnectNotImplemented
}

// SpaceAttempt is the connect ID for this workspace's connect to target in
// space: the one already kept, or fresh, kept now, so a retry after a
// restart repeats the same attempt.
func (s *Store) SpaceAttempt(ctx context.Context, space, target string, fresh [16]byte) ([16]byte, error) {
	return [16]byte{}, errConnectNotImplemented
}

// EndSpaceAttempt forgets the attempt once its contact is installed.
func (s *Store) EndSpaceAttempt(ctx context.Context, space, target string) error {
	return errConnectNotImplemented
}
