// Package space is a node's Pairbook client: invite links, the pins a node
// keeps for the spaces it is in, and signed calls to one directory. It
// returns only records that verify against a pin. It imports nothing of the
// node but the public contract, so the store and the hub can both use it.
package space

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"
)

// MaxResponse bounds a Pairbook response body.
const MaxResponse = 1 << 20

var (
	// ErrInvite is a malformed or non-canonical invite link.
	ErrInvite = errors.New("space: invalid invite link")
	// ErrPinConflict is a pin for a known space with another owner or directory.
	ErrPinConflict = errors.New("space: space already pinned to another owner or directory")
	// ErrDirectory is a pin or invite for another directory than the client's.
	ErrDirectory = errors.New("space: pin or invite names another directory")
	// ErrBase is a directory origin that isn't https://HOST[:PORT] exactly.
	ErrBase = errors.New("space: directory must be https://HOST[:PORT]")
	// ErrUntrusted is a directory answer that doesn't verify against the pin.
	ErrUntrusted = errors.New("space: directory record does not verify")
)

// Pin is what a node keeps for a space it created or joined: the owner key
// it trusts and the directory it uses. A pin is never replaced.
type Pin struct {
	Space    string    // 32 lowercase hex
	Owner    string    // owner key ID, sha256:HEX
	Base     string    // directory origin, https://HOST[:PORT]
	JoinedAt time.Time // when pinned
}

// Names: the pin is to the owner and directory the invite names.
func (p Pin) Names(inv Invite) bool { return p.Owner == inv.Owner && p.Base == inv.Base }

// Invite is a parsed invite link:
//
//	https://HOST[:PORT]/v0/spaces/ID#token=B64URL&owner=sha256:HEX
//
// The fragment never leaves the node: the token goes only in the join
// body, the owner only into the pin.
type Invite struct {
	Base  string
	Space string
	Token [32]byte
	Owner string
}

const spacesPath = "/v0/spaces/"

var b64 = base64.RawURLEncoding.Strict()

// ParseInvite reads a canonical invite link: https, no userinfo or query,
// a canonical space ID, token and owner key ID. Canonical means the link
// is exactly what String writes for the fields it holds.
func ParseInvite(link string) (Invite, error) {
	inv, ok := splitInvite(link)
	if !ok || !inv.canonical(link) {
		return Invite{}, ErrInvite
	}
	return inv, nil
}

// splitInvite reads link's parts, valid or not; false when the fragment
// isn't a token and an owner.
func splitInvite(link string) (Invite, bool) {
	head, fragment, _ := strings.Cut(link, "#")
	base, id, _ := strings.Cut(head, spacesPath)
	token, owner, ok := inviteFragment(fragment)
	inv := Invite{Base: base, Space: id, Owner: owner}
	return inv, ok && decodeToken(token, &inv.Token)
}

// canonical: every field is valid and link is exactly what String writes.
func (i Invite) canonical(link string) bool {
	return checkBase(i.Base) == nil && validSpaceID(i.Space) && validKeyID(i.Owner) && i.String() == link
}

// decodeToken decodes unpadded base64url of exactly a token's size.
func decodeToken(s string, into *[32]byte) bool {
	raw, err := b64.DecodeString(s)
	if err != nil || len(raw) != len(into) {
		return false
	}
	copy(into[:], raw)
	return true
}

// inviteFragment is the token and owner of "token=T&owner=O".
func inviteFragment(fragment string) (string, string, bool) {
	tokenField, ownerField, _ := strings.Cut(fragment, "&")
	token, tokenOK := strings.CutPrefix(tokenField, "token=")
	owner, ownerOK := strings.CutPrefix(ownerField, "owner=")
	return token, owner, tokenOK && ownerOK
}

// String is the invite's canonical link.
func (i Invite) String() string {
	return i.Base + spacesPath + i.Space + "#token=" + b64.EncodeToString(i.Token[:]) + "&owner=" + i.Owner
}

// checkBase: base is an https origin and nothing more.
func checkBase(base string) error {
	u, err := url.Parse(base)
	if err != nil || !isOrigin(u, base) {
		return ErrBase
	}
	return nil
}

// isOrigin: base is exactly https://HOST[:PORT], so no userinfo, path,
// query or fragment.
func isOrigin(u *url.URL, base string) bool {
	return u.Scheme == "https" && u.Host != "" && base == "https://"+u.Host
}

// validSpaceID: 32 lowercase hex characters.
func validSpaceID(id string) bool { return len(id) == 32 && isLowerHex(id) }

// validKeyID: "sha256:" and 64 lowercase hex characters.
func validKeyID(id string) bool {
	hex, ok := strings.CutPrefix(id, "sha256:")
	return ok && len(hex) == 64 && isLowerHex(hex)
}

func isLowerHex(s string) bool { return strings.Trim(s, "0123456789abcdef") == "" }
