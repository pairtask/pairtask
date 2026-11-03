package chat

import (
	"slices"
	"strings"
	"unicode"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Naming a paired workspace in AGENT@WORKSPACE (#297): by what this
// operator calls it, the name it gives itself, its full key, or a key
// prefix of at least 12 hex digits. A name must pick out one workspace
// among all of them, whatever kind of name it is to the others; the
// display title, cut to fit, never names one.

// keyPrefixDigits is the shortest key prefix that names a workspace.
const keyPrefixDigits = 12

// pairedName is a word naming a paired workspace: one of its names, its
// key or a key prefix; identityKey is a contact's full key.
type (
	pairedName  string
	identityKey string
)

// pairedContacts are the contacts as the pairing snapshot last listed them.
func (m *Model) pairedContacts() []protocol.PairingContactView {
	if s := m.pairing.snapshot; s != nil {
		return s.Contacts
	}
	return nil
}

// pairedWorkspace is the one contact a name picks out, by key, or why
// none does.
func (m *Model) pairedWorkspace(name pairedName) (identityKey, string) {
	contacts := m.pairedContacts()
	var keys []identityKey
	for _, c := range contacts {
		if namesContact(c, name) {
			keys = append(keys, identityKey(c.IdentityKey))
		}
	}
	switch len(keys) {
	case 0:
		return "", "no paired workspace " + safe(string(name))
	case 1:
		return keys[0], ""
	}
	tokens := make([]string, len(keys))
	for i, key := range keys {
		tokens[i] = safe(string(routeToken(contacts, key)))
	}
	return "", safe(string(name)) + " names several paired workspaces: " + strings.Join(tokens, ", ")
}

// namesContact: name is one of the contact's names, its key, or a long
// enough prefix of its key.
func namesContact(c protocol.PairingContactView, name pairedName) bool {
	if name == "" {
		return false
	}
	if slices.Contains([]string{c.LocalName, c.Name, c.IdentityKey}, string(name)) {
		return true
	}
	return keyPrefix(name) && strings.HasPrefix(c.IdentityKey, string(name))
}

// keyPrefix: "sha256:" and at least keyPrefixDigits hex digits.
func keyPrefix(name pairedName) bool {
	digits, ok := strings.CutPrefix(string(name), "sha256:")
	return ok && len(digits) >= keyPrefixDigits && strings.Trim(digits, "0123456789abcdef") == ""
}

// routeToken is the shortest name that picks out this contact alone: what
// the operator calls it, else its own name, else the shortest key prefix
// nothing else answers to, up to the whole key. A name a command word
// can't carry (an @, a quote or a backslash) never routes.
func routeToken(contacts []protocol.PairingContactView, key identityKey) pairedName {
	for _, c := range contacts {
		if identityKey(c.IdentityKey) != key {
			continue
		}
		for _, name := range []pairedName{pairedName(c.LocalName), pairedName(c.Name)} {
			if routable(name) && namedOnce(contacts, name) {
				return name
			}
		}
	}
	return uniqueKeyPrefix(contacts, key)
}

// routable: a name a /review word can carry, quoted if it has a space.
func routable(name pairedName) bool {
	return name != "" && !strings.ContainsAny(string(name), `@"\`)
}

// namedOnce: exactly one contact answers to name, by any of its names.
func namedOnce(contacts []protocol.PairingContactView, name pairedName) bool {
	n := 0
	for _, c := range contacts {
		if namesContact(c, name) {
			n++
		}
	}
	return n == 1
}

// uniqueKeyPrefix is the shortest key prefix only this contact answers
// to, by any of everyone's names: one that is another's name is no use.
func uniqueKeyPrefix(contacts []protocol.PairingContactView, key identityKey) pairedName {
	for size := len("sha256:") + keyPrefixDigits; size < len(key); size++ {
		if prefix := pairedName(key[:size]); namedOnce(contacts, prefix) {
			return prefix
		}
	}
	return pairedName(key)
}

// pairedReviewerWords are the paired workspaces' agents a request can
// name, each as @AGENT@TOKEN, quoted when the token has a space; only
// those starting with current.
func (m *Model) pairedReviewerWords(current string) []string {
	contacts := m.pairedContacts()
	var out []string
	for _, c := range contacts {
		key := identityKey(c.IdentityKey)
		token := routeToken(contacts, key)
		for _, agent := range m.theirDirectory(key) {
			mention := "@" + agent + "@" + string(token)
			if strings.HasPrefix(mention, strings.TrimPrefix(current, `"`)) && !strings.ContainsAny(agent, `"\`) {
				out = append(out, pairedWord(mention)) // matched as typed, inserted as one word
			}
		}
	}
	return out
}

// pairedWord is a mention as one /review word: quoted when it has a space.
func pairedWord(mention string) string {
	if strings.ContainsFunc(mention, unicode.IsSpace) {
		return `"` + mention + `"`
	}
	return mention
}
