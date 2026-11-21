package pairbook

import (
	"bytes"
	"crypto/ed25519"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
)

// People (Pairbook v1). A space's members are people, each with the
// workspaces they bound. Everything about who is in a space and who may
// connect to whom lives in one owner-signed SpaceMap (spacemap.go); this
// file holds the person-level parts the map carries: a person's identity
// evidence and their workspace bindings.
//
// Every exported field is wire bytes or a plain key, so a module outside
// pairtask can build and check these records.

// Lineage names a person across key changes: SHA-256 of their identity
// anchor's signed bytes.
type Lineage [32]byte

// PersonEvidence is what establishes a person's current key and epoch:
// their marshalled identity anchor, the identity statements after it
// (records, successions, handovers) in order, and the public keys of every
// workspace those statements name, current or past, so a record's
// workspace bindings can be checked.
type PersonEvidence struct {
	Anchor     []byte
	Statements [][]byte
	Workspaces []ed25519.PublicKey
}

// personState is what a person's evidence establishes.
type personState struct {
	lineage Lineage
	person  string // current person key ID
	key     ed25519.PublicKey
	epoch   uint64
}

// state replays the evidence through the identity rules at at. Any
// statement that doesn't apply cleanly, or a frozen lineage, makes the
// evidence invalid.
func (e PersonEvidence) state(at time.Time) (personState, error) {
	a, err := identity.ParseAnchor(e.Anchor)
	if err != nil {
		return personState{}, ErrInvalid
	}
	h, eff, err := identity.Held{Workspaces: e.workspaceKeys()}.Apply(a, at)
	if err != nil || eff.Kind != identity.Applied {
		return personState{}, ErrInvalid
	}
	for _, b := range e.Statements {
		if h, err = applyEvidence(h, b, at); err != nil {
			return personState{}, err
		}
	}
	if h.Trust == identity.Frozen {
		return personState{}, ErrInvalid
	}
	return personState{lineage: Lineage(h.Lineage), person: h.Person, key: slices.Clone(h.Key(h.Person)), epoch: h.Epoch}, nil
}

// workspaceKeys indexes the evidence's workspace keys by ID, copied.
func (e PersonEvidence) workspaceKeys() map[string]ed25519.PublicKey {
	ws := map[string]ed25519.PublicKey{}
	for _, k := range e.Workspaces {
		if id := KeyID(k); id != "" {
			ws[id] = slices.Clone(k)
		}
	}
	return ws
}

func applyEvidence(h identity.Held, b []byte, at time.Time) (identity.Held, error) {
	st, err := identity.Parse(b)
	if err != nil {
		return h, ErrInvalid
	}
	next, eff, err := h.Apply(st, at)
	if err != nil || eff.Kind != identity.Applied {
		return h, ErrInvalid
	}
	return next, nil
}

// Binding says workspace W belongs to a person: the person's signed claim
// and W's signed acceptance of the same lineage, epoch, workspace and time,
// with W's public key (its ID must be the W both name).
type Binding struct {
	Claim        []byte
	Acceptance   []byte
	WorkspaceKey ed25519.PublicKey
}

// NewBinding binds workspace to the person in lineage l at epoch, at. It
// needs both private keys, so it is for a workspace on the person's own
// machine; a linked machine supplies its acceptance separately.
func NewBinding(person, workspace ed25519.PrivateKey, l Lineage, epoch uint64, at time.Time) Binding {
	wpub := publicOf(workspace)
	w := KeyID(wpub)
	claim := identity.Claim{Lineage: identity.Lineage(l), Epoch: epoch, Workspace: w, At: at}.Signed(person)
	accept := identity.Acceptance{Lineage: identity.Lineage(l), Epoch: epoch, Person: KeyID(publicOf(person)), Workspace: w, At: at}.Signed(workspace)
	return Binding{Claim: claim.Marshal(), Acceptance: accept.Marshal(), WorkspaceKey: wpub}
}

// Workspace is the bound workspace's key ID ("" for a malformed binding).
func (b Binding) Workspace() string { return KeyID(b.WorkspaceKey) }

// check verifies the binding for person p as of notAfter: both signatures,
// matching fields, p's current epoch and key, W's own key, and neither half
// dated after notAfter.
func (b Binding) check(p personState, notAfter time.Time) error {
	c, err := identity.ParseClaim(b.Claim)
	if err != nil || c.Check(p.key) != nil {
		return ErrInvalid
	}
	a, err := identity.ParseAcceptance(b.Acceptance)
	if err != nil || a.Check(b.WorkspaceKey) != nil {
		return ErrInvalid
	}
	if !b.matches(c, a, p) || c.At.After(notAfter) || a.At.After(notAfter) {
		return ErrInvalid
	}
	return nil
}

func (b Binding) matches(c identity.Claim, a identity.Acceptance, p personState) bool {
	w := b.Workspace()
	same := c.Lineage == a.Lineage && c.Epoch == a.Epoch && c.Workspace == a.Workspace && c.At.Equal(a.At)
	return same && w != "" && c.Workspace == w && Lineage(c.Lineage) == p.lineage && c.Epoch == p.epoch && a.Person == p.person
}

// sameBinding: a and b are the same binding, byte for byte.
func sameBinding(a, b Binding) bool {
	return bytes.Equal(a.Claim, b.Claim) && bytes.Equal(a.Acceptance, b.Acceptance) && sameKey(a.WorkspaceKey, b.WorkspaceKey)
}
