package protocol

import (
	"encoding/base64"
	"slices"

	"github.com/pairtask/pairtask/internal/signing"
)

// Signed messages (#115). An operator's messages carry a seal: a header
// (message ID, sender, recipients, room, thread, time, text hash) signed by
// a device key the operator's person key certified, made by the operator's
// own client so no agent and no broker can produce one. The broker checks
// and keeps the seal; each recipient verifies it against the roots it
// knows and reports the outcome itself.

const (
	// SealVersion gates seals, verification and the signing roots.
	SealVersion = 29

	// TypeOperatorIdentity is operator → broker: register the operator's
	// person key (op register), or read the signing roots (op status).
	TypeOperatorIdentity = "operator_identity"
	// TypeOperatorIdentityResult answers it.
	TypeOperatorIdentityResult = "operator_identity_result"

	IdentityOpRegister = "register"
	IdentityOpStatus   = "status"

	// ErrInvalidSeal refuses a message whose seal doesn't verify or
	// doesn't match the message.
	ErrInvalidSeal = "invalid_seal"
)

// Verification is a recipient's verdict on a message's seal: verified,
// unverified, unsigned or invalid, the signer and its basis.
type Verification = signing.Result

// SigningRoot is a key this workspace pinned for verifying seals: its own
// workspace key, or its operator's person key.
type SigningRoot struct {
	KeyID     string `json:"key_id"`
	Person    bool   `json:"person,omitempty"`
	Workspace string `json:"workspace"`
	Approved  bool   `json:"approved"`
}

// IdentityView answers operator_identity: this workspace's key and its
// signing roots.
type IdentityView struct {
	Workspace string        `json:"workspace"`
	Roots     []SigningRoot `json:"roots"`
}

// RootsOf is roots as a lookup for signing.Verify.
func RootsOf(roots []SigningRoot) signing.Roots {
	return func(keyID string) (signing.Root, bool) {
		for _, r := range roots {
			if r.KeyID == keyID {
				return signing.Root{Person: r.Person, Workspace: r.Workspace, Approved: r.Approved}, true
			}
		}
		return signing.Root{}, false
	}
}

// EncodeSeal is a seal as a frame carries it.
func EncodeSeal(s signing.Seal) string { return base64.StdEncoding.EncodeToString(s.Marshal()) }

// DecodeSeal reads a frame's seal; it checks shape, not trust.
func DecodeSeal(text string) (signing.Seal, error) {
	b, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return signing.Seal{}, err
	}
	return signing.ParseSeal(b)
}

// SealRecipients is who an operator message is to, as its seal names
// them: everyone ("*"), one agent, or a group's members sorted and
// without repeats, as the broker canonicalises them; and its room, if any.
func SealRecipients(to string, group []string, room string) []string {
	recipients := []string{to}
	if to == "" {
		recipients = slices.Compact(slices.Sorted(slices.Values(group)))
	}
	if room != "" {
		recipients = append(recipients, signing.RoomRecipient(room))
	}
	slices.Sort(recipients)
	return recipients
}
