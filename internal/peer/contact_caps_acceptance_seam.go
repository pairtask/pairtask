package peer

import "errors"

// Compile-only seam for the frozen #276 contact cap tests.
const (
	MaxActiveContacts     = 1024
	MaxRetainedIdentities = 2048
)

// ErrIdentityStorage: no room to keep another identity.
var ErrIdentityStorage = errors.New("identity storage full; purge needed")
