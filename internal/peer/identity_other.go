//go:build !unix

package peer

import (
	"context"
	"io"
)

// LoadOrCreateIdentity needs unix file ownership and no-follow opens to keep
// the key private; elsewhere there is no workspace identity.
func LoadOrCreateIdentity(context.Context, string, IdentityMarker, io.Reader) (*WorkspaceIdentity, error) {
	return nil, ErrIdentityUnavailable
}
