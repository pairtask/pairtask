package chat

import (
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// A paired workspace's person key (#115): its operator's messages carry
// that operator's own signature, under a key this side has never seen. The
// ⇄ tab names the key while it awaits acceptance; /pair accept-key in the
// tab accepts it, the operator's approval, after comparing it with the
// other operator by phone. Until then those messages show unverified.

const (
	pairAcceptKey          = "accept-key"
	kindContactAcceptKey   = "contact accept key"
	pairRequireSeals       = "require-seals"
	kindContactRequireSeal = "contact require seals"
)

// acceptKeyCommand accepts the open tab's pending person key: the one it
// names, or the only one there is.
func (m *Model) acceptKeyCommand(fields pairWords) tea.Cmd {
	key := m.conv.selected
	if !key.isContact() {
		m.status = "open the workspace's ⇄ tab, then /pair accept-key"
		return nil
	}
	person, problem := chosenKey(m.contacts.thread(key.contact).pendingKeys, fields[1:])
	if problem != "" {
		m.status = problem
		return nil
	}
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpAcceptKey, ContactKey: key.contact,
		MessageID: uuid.NewString(), ContactText: person}
	m.status = "accepting the person key…"
	return m.request(kindContactAcceptKey, msg, key.contact)
}

// chosenKey is the pending key to accept, or why there is none.
func chosenKey(pending, args []string) (string, string) {
	switch {
	case len(args) == 1 && slices.Contains(pending, args[0]):
		return args[0], ""
	case len(args) == 1:
		return "", "this workspace hasn't signed with " + safe(args[0]) + " here"
	case len(pending) == 1:
		return pending[0], ""
	case len(pending) == 0:
		return "", "no person key of this workspace awaits acceptance"
	}
	return "", "several person keys await acceptance: /pair accept-key KEY"
}

// contactAcceptKeyResult takes the page the acceptance answered with.
func (m *Model) contactAcceptKeyResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	contact := req.ids[0]
	if !pageFor(resp, contact) {
		m.status = "accept-key refused: " + refusalReason(resp)
		return nil
	}
	m.contacts.thread(contact).pendingKeys = resp.ContactPage.PendingPersonKeys
	m.status = "person key accepted: this workspace's operator messages verify from now on"
	m.signing.asked = false // the roots changed
	return tea.Batch(m.refreshContact(contact, 0), m.readRoots())
}

// requireSealsCommand turns on or off refusing the open tab's workspace's
// unsigned messages.
func (m *Model) requireSealsCommand(fields pairWords) tea.Cmd {
	key := m.conv.selected
	switch {
	case !key.isContact():
		m.status = "open the workspace's ⇄ tab, then /pair require-seals on|off"
		return nil
	case len(fields) != 2 || (fields[1] != "on" && fields[1] != "off"):
		m.status = "usage: /pair require-seals on|off"
		return nil
	}
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpRequireSeals, ContactKey: key.contact,
		MessageID: uuid.NewString(), ContactText: fields[1]}
	m.status = "require-seals " + fields[1] + "…"
	return m.request(kindContactRequireSeal, msg, key.contact)
}

// contactRequireSealsResult takes the page the change answered with.
func (m *Model) contactRequireSealsResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	contact := req.ids[0]
	if !pageFor(resp, contact) {
		m.status = "require-seals refused: " + refusalReason(resp)
		return nil
	}
	m.contacts.thread(contact).requireSeals = resp.ContactPage.RequireSeals
	m.status = "this workspace's unsigned messages are taken, marked unsigned"
	if resp.ContactPage.RequireSeals {
		m.status = "this workspace's unsigned messages are refused from now on"
	}
	return m.refreshContact(contact, 0)
}

// pendingKeyLine names the person keys a workspace awaits acceptance for,
// and says when its unsigned messages are refused.
func pendingKeyLine(t *contactThread) string {
	switch {
	case t == nil:
		return ""
	case len(t.pendingKeys) == 0 && t.requireSeals:
		return "requires signatures: unsigned messages from this workspace are refused"
	case len(t.pendingKeys) == 0:
		return ""
	case len(t.pendingKeys) == 1:
		return "signs as person " + safe(t.pendingKeys[0]) + ", not yet accepted: compare it with them, then /pair accept-key"
	}
	return "signs as several person keys, none accepted: compare with them, then /pair accept-key KEY"
}
