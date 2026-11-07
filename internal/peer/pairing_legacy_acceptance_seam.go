package peer

import "errors"

var ErrPeerTooOld = errors.New("peer: the other side must update to pair again")

// Legacy delegate: no declared repair is captured or propagated.
func (iv *Invitations) OpenRepair(key string) (Invitation, error) { return iv.Open() }
