package hub

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/pairbook"
)

// BindingAcceptance is this workspace's half of binding it to a person:
// the workspace key's signed acceptance of the person's claim, and the
// key. Only the operator socket asks for it.
type BindingAcceptance struct {
	Acceptance   []byte
	WorkspaceKey ed25519.PublicKey
}

// ErrBindingRequest: a malformed lineage or person key ID.
var ErrBindingRequest = errors.New("hub: invalid binding request")

// BindingRequest is a person's claim this workspace accepts: their lineage
// (hex), the epoch, their person key ID, and when.
type BindingRequest struct {
	Lineage string
	Epoch   uint64
	Person  string
	At      time.Time
}

// AcceptBinding signs this workspace's acceptance of belonging to the
// person r names.
func (h *Hub) AcceptBinding(r BindingRequest) (BindingAcceptance, error) {
	l, err := r.lineage()
	if err != nil {
		return BindingAcceptance{}, err
	}
	ep, err := h.PairingEndpoint()
	if err != nil {
		return BindingAcceptance{}, err
	}
	pub := ep.Identity.PublicKey()
	a := identity.Acceptance{Lineage: l, Epoch: r.Epoch, Person: r.Person, Workspace: pairbook.KeyID(pub), At: r.At}
	sig, err := ep.Identity.Sign(rand.Reader, a.TBS(), crypto.Hash(0))
	if err != nil {
		return BindingAcceptance{}, err
	}
	a.Sig = sig
	return BindingAcceptance{Acceptance: a.Marshal(), WorkspaceKey: pub}, nil
}

// lineage is r's lineage, once every field is one the identity parser
// will take back: canonical hex, a canonical key ID, an epoch in range, a
// time not before 1970.
func (r BindingRequest) lineage() (identity.Lineage, error) {
	var l identity.Lineage
	b, ok := canonicalHex(r.Lineage)
	if !ok || len(b) != len(l) {
		return l, ErrBindingRequest
	}
	if !r.signable() {
		return l, ErrBindingRequest
	}
	copy(l[:], b)
	return l, nil
}

func (r BindingRequest) signable() bool {
	if !r.inRange() {
		return false
	}
	h, ok := strings.CutPrefix(r.Person, "sha256:")
	b, canonical := canonicalHex(h)
	return ok && canonical && len(b) == 32
}

// inRange: the epoch and time fit what the identity parser takes.
func (r BindingRequest) inRange() bool {
	epochOK := r.Epoch != 0 && r.Epoch <= identity.MaxCounter
	return epochOK && r.At.UnixMilli() >= 0
}

// canonicalHex decodes lowercase hex, and only that.
func canonicalHex(s string) ([]byte, bool) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return b, hex.EncodeToString(b) == s
}
