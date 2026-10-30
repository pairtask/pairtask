package chat

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Agents across paired workspaces (#222), in a contact's ⇄ tab: a draft
// starting with @agent words goes to those agents of the contact instead
// of its operator, and /pair share or unshare decides which agents here
// the contact may message. Agent names a contact sent are shown escaped,
// like its text.

const (
	kindContactShare = "contact share"
	pairShare        = protocol.ContactOpShare
	pairUnshare      = protocol.ContactOpUnshare
)

// routingAvailable: the broker routes contact messages to and from agents.
func (m *Model) routingAvailable() bool {
	return m.session.HasCapability(protocol.CapabilityContactRouting)
}

// sharesAgents: /pair share and unshare name agents after the verb.
func sharesAgents(verb string) bool { return verb == pairShare || verb == pairUnshare }

// contactAddress splits a draft's leading @agent words from its text: the
// contact's agents it is to, none for its operator.
func contactAddress(draft string) ([]string, string) {
	var to []string
	rest := strings.TrimLeft(draft, " ")
	for strings.HasPrefix(rest, "@") {
		word, after, _ := strings.Cut(rest, " ")
		to = append(to, strings.TrimPrefix(word, "@"))
		rest = strings.TrimLeft(after, " ")
	}
	return to, rest
}

// addressProblem is why an addressed draft can't be sent, or "".
func (m *Model) addressProblem(to []string, text string) string {
	switch {
	case len(to) == 0:
		return ""
	case !m.routingAvailable():
		return "this broker can't send to a workspace's agents; upgrade it"
	case strings.TrimSpace(text) == "":
		return "write the message after the @agents"
	case protocol.ValidateContactRequest(protocol.Message{ContactOp: protocol.ContactOpSend, ContactKey: "sha256:" + strings.Repeat("0", 64),
		MessageID: "check", ContactText: text, ContactTo: to}) != nil:
		return "@ names up to 64 distinct agents of that workspace"
	}
	return ""
}

// shareCommand is /pair share or unshare in a contact's tab: the agents
// named here, or * for all, may message that workspace's side, or no more.
func (m *Model) shareCommand(fields pairWords) tea.Cmd {
	key := m.conv.selected
	switch {
	case !m.routingAvailable():
		m.status = "this broker can't share agents with a workspace; upgrade it"
		return nil
	case !key.isContact():
		m.status = "open the workspace's ⇄ tab, then /pair " + fields[0]
		return nil
	case !m.confirmedContact(key.contact):
		m.status = contactConfirming
		return nil
	}
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: fields[0], ContactKey: key.contact,
		MessageID: uuid.NewString(), ContactAgents: fields[1:]}
	m.status = fields[0] + "…"
	return m.request(kindContactShare, msg, key.contact, fields[0])
}

// contactShareResult shows what is shared now.
func (m *Model) contactShareResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	contact, verb := req.ids[0], req.ids[1]
	if !pageFor(resp, contact) {
		m.status = verb + " refused: " + refusalReason(resp)
		return nil
	}
	t := m.contacts.thread(contact)
	t.shared, t.directory = resp.ContactPage.Shared, resp.ContactPage.Directory
	m.status = "shared with this workspace: " + agentNames(t.shared, "no agents")
	return nil
}

// sharingLine is what each side shares, for the contact's card.
func (m *Model) sharingLine(fingerprint string) string {
	t := m.contacts.threads[fingerprint]
	if t == nil || !t.loaded {
		return ""
	}
	return "you share " + agentNames(t.shared, "no agents (/pair share AGENT)") +
		" · they share " + agentNames(t.directory, "none") + " · @agent text writes to theirs"
}

// agentNames is a list of agents, escaped, or none when empty.
func agentNames(agents []string, none string) string {
	if len(agents) == 0 {
		return none
	}
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = safe(a)
	}
	return strings.Join(names, ", ")
}

// routeHead is a message's agents for its first line: who sent it there,
// whom it is to, and which of them acknowledged it.
func routeHead(v protocol.ContactMessageView) string {
	head := ""
	if v.From != "" {
		head += " · " + safe(v.From)
	}
	if len(v.To) > 0 {
		head += " → @" + strings.Join(escapedAll(v.To), ", @")
	}
	if len(v.AgentAcks) > 0 {
		head += " · acknowledged by " + agentNames(v.AgentAcks, "")
	}
	return head
}

func escapedAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = safe(s)
	}
	return out
}
