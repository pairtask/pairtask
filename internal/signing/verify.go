package signing

import (
	"crypto/ed25519"
	"errors"
	"slices"
	"strings"
	"time"
)

// State is what a recipient can tell about who sent a message.
type State string

const (
	// Verified: the signature holds, up to a root the recipient approved.
	Verified State = "verified"
	// Unverified: the signature holds, but its root isn't approved, or
	// isn't known here.
	Unverified State = "unverified"
	// Unsigned: no signature.
	Unsigned State = "unsigned"
	// Invalid: a signature that doesn't hold, or claims what its key can't.
	Invalid State = "invalid"
)

// Basis is what vouches for a signer.
type Basis string

const (
	// BasisWorkspace: the sender's workspace (its broker) attests the agent.
	BasisWorkspace Basis = "workspace"
	// BasisAgent: the agent's own key, certified by its workspace.
	BasisAgent Basis = "agent"
	// BasisPerson: a person's key certified the device that signed.
	BasisPerson Basis = "person"
)

// Signer is who a signature speaks for, and on what basis.
type Signer struct {
	Kind      string `json:"kind"` // SenderAgent or SenderOperator
	ID        string `json:"id"`
	Workspace string `json:"workspace_key_id"`
	Basis     Basis  `json:"basis"`
	KeyID     string `json:"key_id"`
}

// Result is a verification's outcome.
type Result struct {
	State  State   `json:"state"`
	Signer *Signer `json:"signer,omitempty"`
	Reason string  `json:"reason,omitempty"`
}

// Root is a key the recipient pinned, by fingerprint: a workspace's
// identity key or a person's key, the workspace it belongs to, and whether
// the operator approved it. The key itself travels in the seal.
type Root struct {
	Person    bool
	Workspace string
	Approved  bool
}

// Roots finds a pinned root by key ID.
type Roots func(keyID string) (Root, bool)

var (
	errChain     = errors.New("signing: certificate chain is not one certificate from a root")
	errRootKey   = errors.New("signing: root key doesn't match the certificate's issuer")
	errClaim     = errors.New("signing: the key doesn't speak for this sender")
	errWorkspace = errors.New("signing: certificate is for another workspace")
	errFrame     = errors.New("signing: the message doesn't match what was signed")
)

// MaxSkew is how far ahead of the recipient's clock a header may be dated.
const MaxSkew = 5 * time.Minute

var errFuture = errors.New("signing: signed in the future")

// Verify checks a seal against the recipient's roots. Everything the seal
// itself can prove is checked first, pinned or not: the signature, the
// certificate (by the root key the seal carries, valid when the header was
// signed) and whether its kind may speak for the claimed sender. Only then
// does the pin decide: unknown or unapproved is Unverified, a pin that
// disagrees with the certificate (a person key signing for agents, another
// workspace) is Invalid. A stored message still verifies after its
// certificate lapses; revocation, not expiry, ends a stolen key's past.
func Verify(s *Seal, roots Roots, now time.Time) Result {
	if s == nil {
		return Result{State: Unsigned}
	}
	signer, err := provable(*s, now)
	if err != nil {
		return Result{State: Invalid, Reason: err.Error()}
	}
	root, known := roots(signer.issuer)
	switch {
	case !known:
		return Result{State: Unverified, Signer: &signer.Signer, Reason: "signed under a key not pinned here"}
	case root.Person != (signer.Basis == BasisPerson) || root.Workspace != s.Header.Workspace:
		return Result{State: Invalid, Reason: errClaim.Error()}
	case !root.Approved:
		return Result{State: Unverified, Signer: &signer.Signer, Reason: "signer's key awaits the operator's approval"}
	}
	return Result{State: Verified, Signer: &signer.Signer}
}

// provenSigner is who a seal proves signed it, and the root key it
// claims to be issued under.
type provenSigner struct {
	Signer
	issuer string
}

// provable checks what a seal proves on its own, before any pin.
func provable(s Seal, now time.Time) (provenSigner, error) {
	if s.Header.CreatedAt.After(now.Add(MaxSkew)) {
		return provenSigner{}, errFuture
	}
	leaf, err := signedBy(s)
	if err != nil {
		return provenSigner{}, err
	}
	if err := leaf.Check(s.RootKey, s.Header.CreatedAt); err != nil {
		return provenSigner{}, err
	}
	basis, ok := claimBasis(s.Header, leaf, s.RootKey)
	if !ok {
		return provenSigner{}, errClaim
	}
	h := s.Header
	return provenSigner{Signer{Kind: h.SenderKind, ID: h.SenderID, Workspace: h.Workspace, Basis: basis, KeyID: leaf.KeyID()}, leaf.Issuer}, nil
}

// signedBy is the certificate whose key signed the header; phase 1 chains
// are one certificate issued straight by the root the seal carries.
func signedBy(s Seal) (Cert, error) {
	if len(s.Chain) != 1 {
		return Cert{}, errChain
	}
	leaf := s.Chain[0]
	if leaf.KeyID() != s.KeyID || !ed25519.Verify(leaf.Key, s.Header.tbs(), s.Sig) {
		return Cert{}, ErrBadSignature
	}
	if KeyID(s.RootKey) != leaf.Issuer {
		return Cert{}, errRootKey
	}
	if leaf.Workspace != s.Header.Workspace {
		return Cert{}, errWorkspace
	}
	return leaf, nil
}

// OperatorID is the reserved sender ID of a workspace's operator.
const OperatorID = "operator"

// claimBasis is the basis on which leaf may sign h, if any: a device
// speaks only for its operator (under a person key), a workspace's broker
// only for that workspace's agents and an agent key only for itself (both
// under the workspace key itself).
func claimBasis(h Header, leaf Cert, rootKey ed25519.PublicKey) (Basis, bool) {
	if leaf.Kind == KindDevice {
		return BasisPerson, h.SenderKind == SenderOperator && h.SenderID == OperatorID
	}
	if !workspaceAgent(h, rootKey) {
		return "", false
	}
	switch leaf.Kind {
	case KindBroker:
		return BasisWorkspace, true
	case KindAgent:
		return BasisAgent, leaf.Subject == h.SenderID
	}
	return "", false
}

// workspaceAgent: h is from an agent (never the reserved operator ID) of
// the workspace whose key rootKey is.
func workspaceAgent(h Header, rootKey ed25519.PublicKey) bool {
	return h.SenderKind == SenderAgent && h.SenderID != OperatorID && KeyID(rootKey) == h.Workspace
}

// Frame is what a recipient received, to check against the signed header:
// the message, its sender, its text, the addresses that reach this
// recipient (its own ID, "*", its group's members) and the room it arrived
// in ("" for none).
type Frame struct {
	MessageID  string
	SenderKind string
	SenderID   string
	Text       string
	Addresses  []string
	Room       string
}

// RoomRecipient is how a header names the room a message was sent in.
func RoomRecipient(room string) string { return "room:" + room }

// Matches reports whether the received frame is the message the header
// signed and was addressed to this recipient.
func (s Seal) Matches(f Frame) error {
	if !s.Header.signs(f) {
		return errFrame
	}
	return nil
}

// signs: the header is this frame's message, from this sender, with this
// text, addressed to this recipient, in this room.
func (h Header) signs(f Frame) bool {
	same := h.MessageID == f.MessageID && h.SenderKind == f.SenderKind && h.SenderID == f.SenderID && h.TextHash == TextHash(f.Text)
	return same && slices.ContainsFunc(f.Addresses, h.addresses) && h.room() == f.Room
}

// room is the room the header was signed for, or "".
func (h Header) room() string {
	for _, r := range h.Recipients {
		if room, ok := strings.CutPrefix(r, "room:"); ok {
			return room
		}
	}
	return ""
}

func (h Header) addresses(a string) bool { return slices.Contains(h.Recipients, a) }
