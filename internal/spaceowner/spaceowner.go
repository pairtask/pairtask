// Package spaceowner is the person's own host as a space owner: the
// owner logic in pairbook/owner, loaded from the person's keys.
package spaceowner

import (
	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/pairbook/owner"
)

type (
	Me      = owner.Me
	Current = owner.Current
	Genesis = owner.Genesis
	Request = owner.Request
	Outcome = owner.Outcome
	Batch   = owner.Batch
)

const (
	Applied          = owner.Applied
	Unchanged        = owner.Unchanged
	Stale            = owner.Stale
	Rejected         = owner.Rejected
	AwaitingApproval = owner.AwaitingApproval
)

var (
	ErrNotOwner = owner.ErrNotOwner
	ErrOverflow = owner.ErrOverflow
	ErrValidity = owner.ErrValidity
	ErrMark     = owner.ErrMark
)

// NewSpace is a space's first map; see owner.NewSpace.
var NewSpace = owner.NewSpace

// Load is the person whose keys and stored anchor keys holds.
func Load(keys *personkey.Keys) (Me, error) {
	anchor, err := keys.Anchor()
	if err != nil {
		return Me{}, err
	}
	return owner.New(keys.PersonKey(), anchor)
}
