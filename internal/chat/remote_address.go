package chat

import (
	"slices"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Remote agents in the Square (#259): a paired workspace's agent is written
// to from that workspace's ⇄ tab. A leading @AGENT@WORKSPACE here, with the
// workspace named by its exact name or full key, holds the draft and says
// which tab; it is never an agent ID of this workspace. A bare tag stays this
// workspace's agent, and the audience line says so when a paired workspace
// shares an agent of that name.

// remoteTagProblem is why a leading @AGENT@WORKSPACE can't go from here, ""
// when the word is no such tag.
func (m *Model) remoteTagProblem(word string) string {
	t := parseTag(strings.TrimPrefix(word, "@"))
	if !t.scoped || t.scope == "" {
		return ""
	}
	if t.scope == hereScope {
		return safe(t.word) + " names this workspace: tag your agent as @" + safe(t.name)
	}
	c, problem := m.remoteContact(t.scope)
	if problem != "" {
		return safe(t.word) + ": " + problem
	}
	ws := safe(contactDisplay(c))
	return safe(t.name) + "@" + ws + " is " + ws + "'s agent: write it in the " + ws + " ⇄ tab"
}

// remoteContact is the paired workspace a tag's scope names, exactly, and
// one it can be written to; or why not.
func (m *Model) remoteContact(scope string) (protocol.PairingContactView, string) {
	var contacts []protocol.PairingContactView
	if s := m.pairing.snapshot; s != nil {
		contacts = s.Contacts
	}
	found := slices.DeleteFunc(slices.Clone(contacts), func(c protocol.PairingContactView) bool { return !namesExactly(c, scope) })
	switch len(found) {
	case 0:
		return protocol.PairingContactView{}, "unknown workspace " + safe(scope) + "; /pair status lists the paired ones"
	case 1:
		return found[0], unavailable(found[0])
	}
	return protocol.PairingContactView{}, "ambiguous: " + safe(scope) + " names more than one workspace; use its full key"
}

// namesExactly: scope is the contact's full key, or one of its names.
func namesExactly(c protocol.PairingContactView, scope string) bool {
	return c.IdentityKey == scope || slices.Contains([]string{c.LocalName, shownName(c.Name), contactDisplay(c)}, scope)
}

// unavailable is why a contact can't be written to, "" when it can.
func unavailable(c protocol.PairingContactView) string {
	ws := safe(contactDisplay(c))
	switch {
	case unpairedHere(c):
		return ws + " is unpaired: unknown as a workspace to write to"
	case !c.Confirmed:
		return "pairing with " + ws + " isn't confirmed yet"
	case c.PausedAt != nil:
		return ws + " is paused: /pair resume " + ws
	}
	return ""
}

// collisionNote says which agent a bare Square tag means when a paired
// workspace shares the name: this workspace's, and where theirs is.
func (m *Model) collisionNote(mentions []string) string {
	if !m.conv.selected.common() {
		return ""
	}
	var notes []string
	for _, name := range mentions {
		for _, ws := range m.sharingName(name) {
			notes = append(notes, safe(name)+" is your agent here; "+safe(name)+"@"+ws+" is in the "+ws+" ⇄ tab")
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return " · " + strings.Join(notes, "; ")
}

// sharingName are the paired workspaces whose shared agents include name, by
// name, in order.
func (m *Model) sharingName(name string) []string {
	var out []string
	for key, t := range m.contacts.threads {
		if t.dirKnown && slices.Contains(t.directory, name) {
			out = append(out, safe(m.contactTitle(contactKey(key))))
		}
	}
	slices.Sort(out)
	return out
}
