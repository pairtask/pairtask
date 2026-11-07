package store

import (
	"context"
	"errors"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// Compile-only seam for the frozen #276 control-store tests.

// ErrUnpairedByThem: the other side unpaired.
var ErrUnpairedByThem = errors.New("the other side unpaired; unpair here or pair again")

// AdmitControl admits a control session (seam: not implemented).
func (p *PeerMessages) AdmitControl(ctx context.Context, contact string) (messaging.ControlStore, error) {
	return nil, messaging.ErrUnknownContact
}
