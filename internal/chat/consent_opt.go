package chat

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Opting a paired workspace in to consent prompts, and resetting one scope
// to undecided (#293): /pair consent on NAME, off NAME and
// reset NAME AGENT OPERATION. Only the full reset sends anything; with less
// it lists the contact's scopes and the exact command for each.

const (
	consentOn    = "on"
	consentOff   = "off"
	consentReset = "reset"

	kindConsentOpt   = "consent opt"
	kindConsentReset = "consent reset"
)

// consentOperations are the operations a scope reset names, in order.
var consentOperations = []string{protocol.ConsentMessage, protocol.ConsentReview, protocol.ConsentPublish}

// legacyConsentNote is the disclosure for workspaces paired before consent
// prompts: what was never decided there stays denied until reset here.
const legacyConsentNote = "paired before consent prompts? there, scopes never decided stay denied until you reset each one: /pair consent reset NAME AGENT OPERATION"

// consentOptCommand is /pair consent on|off NAME.
func (m *Model) consentOptCommand(fields pairWords) tea.Cmd {
	if len(fields) != 3 {
		m.status = "/pair consent " + fields[1] + " NAME: the paired workspace"
		return nil
	}
	key, problem := m.resolveConsentContact(consentName(fields[2]))
	if problem != "" {
		m.status = problem
		return nil
	}
	on := fields[1] == consentOn
	m.status = "consent " + fields[1] + " for " + safe(m.contactTitle(contactKey(key))) + "…"
	return m.request(kindConsentOpt, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpConsentOpt,
		MessageID: uuid.NewString(), ContactKey: key, ConsentOn: &on}, key, fields[1])
}

// consentOptResult says what holding now means for that workspace.
func (m *Model) consentOptResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	name := safe(m.contactTitle(contactKey(req.ids[0])))
	if resp.Type != protocol.TypeOperatorContactResult {
		m.status = "consent " + req.ids[1] + " for " + name + " refused: " + refusalReason(resp)
		return nil
	}
	if req.ids[1] == consentOff {
		m.status = "consent off for " + name + ": its requests for anything you never decided are refused, as before"
		return nil
	}
	m.status = "consent on for " + name + ": its requests for anything you never decided wait for you under Needs you · " + legacyConsentNote
	return nil
}

// consentResetCommand is /pair consent reset NAME AGENT OPERATION, the
// only form that sends; with less it shows the scopes to choose from.
func (m *Model) consentResetCommand(fields pairWords) tea.Cmd {
	if len(fields) < 3 || len(fields) > 5 {
		m.status = "/pair consent reset NAME AGENT OPERATION: OPERATION is message, review or publish"
		return nil
	}
	key, problem := m.resolveConsentContact(consentName(fields[2]))
	if problem != "" {
		m.status = problem
		return nil
	}
	if len(fields) < 5 {
		m.showResetScopes(key, fields[3:])
		return nil
	}
	return m.sendReset(consentScope{contact: key, agent: fields[3], operation: fields[4]})
}

// consentScope is one agent's operation for one contact.
type consentScope struct{ contact, agent, operation string }

// sendReset sends one scope's reset, once it names a valid agent and
// operation.
func (m *Model) sendReset(s consentScope) tea.Cmd {
	if protocol.ValidateAgentID(s.agent) != nil || !slices.Contains(consentOperations, s.operation) {
		m.status = "/pair consent reset NAME AGENT OPERATION: OPERATION is message, review or publish"
		return nil
	}
	m.status = "resetting " + m.scopeWords(s) + "…"
	return m.request(kindConsentReset, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpConsentReset,
		MessageID: uuid.NewString(), ContactKey: s.contact, ConsentAgent: s.agent, ConsentOperation: s.operation}, s.contact, s.agent, s.operation)
}

// scopeWords name a scope exactly.
func (m *Model) scopeWords(s consentScope) string {
	return safe(s.operation) + " to " + safe(s.agent) + " from " + safe(m.contactTitle(contactKey(s.contact)))
}

// consentResetResult names the exact scope reset, or why it wasn't.
func (m *Model) consentResetResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	s := consentScope{contact: req.ids[0], agent: req.ids[1], operation: req.ids[2]}
	if resp.Type != protocol.TypeOperatorContactResult {
		m.status = "reset of " + m.scopeWords(s) + " refused: " + refusalReason(resp)
		return nil
	}
	m.status = "reset " + m.scopeWords(s) + " to undecided: with consent on, its next request waits for your decision"
	return nil
}

// showResetScopes lists the contact's scopes, or one agent's, with what
// this side knows of each and the command that resets it. Nothing is sent.
func (m *Model) showResetScopes(key string, agent []string) {
	name := m.contactToken(key)
	rows := []string{titleStyle.Render("Reset a scope of "+safe(m.contactTitle(contactKey(key)))+" to undecided") +
		mutedStyle.Render("  (Esc closes; nothing was sent)"),
		mutedStyle.Render("  " + legacyConsentNote)}
	agents := m.scopeAgents(key, agent)
	if len(agents) == 0 {
		rows = append(rows, "  no agents known here: /pair consent reset "+name+" AGENT OPERATION")
	}
	for _, a := range agents {
		for _, op := range consentOperations {
			s := consentScope{contact: key, agent: a, operation: op}
			rows = append(rows, "  "+safe(a)+" "+op+": "+m.scopeState(s)+mutedStyle.Render("  /pair consent reset "+name+" "+safe(a)+" "+op))
		}
	}
	m.openHeldPanel(rows)
	m.status = "choose a scope; only /pair consent reset NAME AGENT OPERATION resets it"
}

// scopeAgents are the agent named, or every agent known here or granted
// to the contact.
func (m *Model) scopeAgents(key string, named []string) []string {
	if len(named) == 1 {
		return named
	}
	agents := m.localAgents("")
	if t := m.contacts.threads[key]; t != nil {
		for _, a := range t.shared {
			agents = appendOnce(agents, a)
		}
		for _, g := range t.grants {
			agents = appendOnce(agents, g.Agent)
		}
	}
	slices.Sort(agents)
	return agents
}

// scopeState is what this side knows of a scope: allowed, denied by an
// explicit off, or not allowed, which the broker doesn't tell apart as
// denied or undecided.
func (m *Model) scopeState(s consentScope) string {
	t := m.contacts.threads[s.contact]
	if t == nil || !t.loaded {
		return "state unavailable here (open its ⇄ tab to load what it may do)"
	}
	if state := grantedState(t, s); state != "" {
		return state
	}
	return "state unavailable: not allowed, denied or undecided (the broker doesn't say which)"
}

// grantedState is "allowed" or "denied (off)" from the contact's shares and
// review grants, or "" when they say neither.
func grantedState(t *contactThread, s consentScope) string {
	if s.operation == protocol.ConsentMessage {
		if slices.Contains(t.shared, s.agent) {
			return "allowed"
		}
		return ""
	}
	i := slices.IndexFunc(t.grants, func(g protocol.ReviewGrantView) bool { return g.Agent == s.agent })
	if i < 0 {
		return ""
	}
	return grantWords(t.grants[i], s.operation)
}

// grantWords say what a review grant allows of an operation.
func grantWords(g protocol.ReviewGrantView, operation string) string {
	switch {
	case g.Mode == "off":
		return "denied (off)"
	case operation == protocol.ConsentReview:
		return "allowed (" + safe(g.Mode) + ")"
	case g.Publish:
		return "allowed"
	}
	return ""
}

// consentCompletions offer /pair consent's next word: its reserved words,
// held contacts and requests, then contacts, agents and operations.
func (m *Model) consentCompletions(l argLine) []string {
	switch len(l.words) {
	case 1:
		return withPrefix(m.consentFirstWords(l.current), l.current)
	case 2:
		return m.consentContactWords(l)
	}
	if l.words[1] != consentReset {
		return nil
	}
	if len(l.words) == 3 {
		return withPrefix(m.localAgents(""), l.current)
	}
	if len(l.words) == 4 {
		return withPrefix(consentOperations, l.current)
	}
	return nil
}

// consentFirstWords are on, off, reset, each held contact and each held
// request.
func (m *Model) consentFirstWords(current string) []string {
	if name, ok := strings.CutPrefix(current, consentContactPrefix); ok && m.namesAny(consentName(name)) {
		return nil // Enter runs what is typed; it never picks one of several
	}
	words := []string{consentOn, consentOff, consentReset}
	for _, key := range m.held.contacts() {
		words = append(words, consentContactPrefix+m.contactToken(key))
	}
	return append(words, m.heldIDs()...)
}

// consentContactWords are the paired workspaces, after on, off or reset.
func (m *Model) consentContactWords(l argLine) []string {
	if _, ok := consentVerbs[l.words[1]]; !ok || m.namesAny(consentName(l.current)) {
		return nil // a typed name runs as typed: Enter never picks one of several
	}
	var tokens []string
	for _, key := range m.knownContacts() {
		tokens = append(tokens, m.contactToken(key))
	}
	slices.Sort(tokens)
	return withPrefix(tokens, l.current)
}
