package signing

import (
	"crypto/ed25519"
	"errors"
	"slices"
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
	State  State   `json:"verification"`
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

// Verify checks a seal against the recipient's roots at now.
func Verify(s *Seal, roots Roots, now time.Time) Result {
	if s == nil {
		return Result{State: Unsigned}
	}
	leaf, err := signedBy(*s)
	if err != nil {
		return Result{State: Invalid, Reason: err.Error()}
	}
	root, known := roots(leaf.Issuer)
	if !known {
		return Result{State: Unverified, Reason: "signed under a key not pinned here"}
	}
	signer, err := speaksFor(s.Header, leaf, s.RootKey, root, now)
	if err != nil {
		return Result{State: Invalid, Reason: err.Error()}
	}
	if !root.Approved {
		return Result{State: Unverified, Signer: signer, Reason: "signer's key awaits the operator's approval"}
	}
	return Result{State: Verified, Signer: signer}
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

// speaksFor checks leaf against its root and what h claims, and names the
// signer: a person's device speaks only for its operator, a workspace's
// broker only for that workspace's agents, an agent key only for itself.
func speaksFor(h Header, leaf Cert, rootKey ed25519.PublicKey, root Root, now time.Time) (*Signer, error) {
	if err := leaf.Check(rootKey, now); err != nil {
		return nil, err
	}
	if root.Workspace != h.Workspace {
		return nil, errWorkspace
	}
	basis, ok := claimBasis(h, leaf, rootKey, root)
	if !ok {
		return nil, errClaim
	}
	return &Signer{Kind: h.SenderKind, ID: h.SenderID, Workspace: h.Workspace, Basis: basis, KeyID: leaf.KeyID()}, nil
}

// OperatorID is the reserved sender ID of a workspace's operator.
const OperatorID = "operator"

// claimBasis is the basis on which leaf may sign h, if any.
func claimBasis(h Header, leaf Cert, rootKey ed25519.PublicKey, root Root) (Basis, bool) {
	switch {
	case root.Person:
		return BasisPerson, leaf.Kind == KindDevice && h.SenderKind == SenderOperator && h.SenderID == OperatorID
	case h.SenderKind != SenderAgent || h.SenderID == OperatorID || KeyID(rootKey) != h.Workspace:
		return "", false
	case leaf.Kind == KindBroker:
		return BasisWorkspace, true
	}
	return BasisAgent, leaf.Kind == KindAgent && leaf.Subject == h.SenderID
}

// Frame is what a recipient received, to check against the signed header:
// the message, its sender, its text, and the addresses that reach this
// recipient (its own ID, "*", its room, its group).
type Frame struct {
	MessageID  string
	SenderKind string
	SenderID   string
	Text       string
	Addresses  []string
}

// Matches reports whether the received frame is the message the header
// signed and was addressed to this recipient.
func (s Seal) Matches(f Frame) error {
	h := s.Header
	same := h.MessageID == f.MessageID && h.SenderKind == f.SenderKind && h.SenderID == f.SenderID && h.TextHash == TextHash(f.Text)
	if !same || !slices.ContainsFunc(f.Addresses, h.addresses) {
		return errFrame
	}
	return nil
}

func (h Header) addresses(a string) bool { return slices.Contains(h.Recipients, a) }
