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
func (f pairWords) sharesAgents() bool { return f[0] == pairShare || f[0] == pairUnshare }

// address is a draft's recipients and text: the contact's agents named by
// its leading @agent words, then the rest; with none, the whole draft goes
// to the contact's operator as written.
type address struct {
	to   []string
	text string
}

func contactAddress(draft string) address {
	var to []string
	rest := strings.TrimLeft(draft, " ")
	for strings.HasPrefix(rest, "@") {
		word, after, _ := strings.Cut(rest, " ")
		to = append(to, strings.TrimPrefix(word, "@"))
		rest = strings.TrimLeft(after, " ")
	}
	if len(to) == 0 {
		return address{text: draft}
	}
	return address{to, rest}
}

// addressProblem is why an addressed draft can't be sent, or "".
func (m *Model) addressProblem(a address) string {
	switch {
	case len(a.to) == 0:
		return ""
	case !m.routingAvailable():
		return "this broker can't send to a workspace's agents; upgrade it"
	case strings.TrimSpace(a.text) == "":
		return "write the message after the @agents"
	case protocol.ValidateContactRequest(protocol.Message{ContactOp: protocol.ContactOpSend, ContactKey: "sha256:" + strings.Repeat("0", 64),
		MessageID: "check", ContactText: a.text, ContactTo: a.to}) != nil:
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
func sharingLine(t *contactThread) string {
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
		head += " → " + agentNames(v.To, "")
	}
	if len(v.AgentAcks) > 0 {
		head += " · acknowledged by " + agentNames(v.AgentAcks, "")
	}
	return head
}
