package store

import (
	"context"
	"errors"
	"time"
)

// ErrUnpaired: the contact's pairing was ended; nothing more is sent or
// taken under it (#276).
var ErrUnpaired = errors.New("the contact is unpaired; pair again to resume")

// errUnpairPending marks the compile-only seam until UnpairContact exists.
var errUnpairPending = errors.New("unpair: not implemented")

// ContactUnpair ends a contact's current pairing under the operator's
// operation ID and audit row. Confirm must repeat the contact exactly and
// SeenEpoch be the pairing epoch the operator was shown.
type ContactUnpair struct {
	OperationID, Contact, Confirm string
	SeenEpoch                     int64
	AuditID                       int64
	At                            time.Time
}

// ContactUnpairResult is what an unpair did: whether it changed the state,
// the generation after it, the pairing epoch it ended and when.
type ContactUnpairResult struct {
	Changed    bool
	Generation int64
	Epoch      int64
	UnpairedAt time.Time
}

// UnpairContact ends the contact's current pairing in one transaction with
// its audit; applied is false when the call answered an earlier result.
func (s *Store) UnpairContact(ctx context.Context, r ContactUnpair) (ContactUnpairResult, bool, error) {
	return ContactUnpairResult{}, false, errUnpairPending
}
