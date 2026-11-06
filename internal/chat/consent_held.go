package chat

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Held requests at a glance (#293): how many each paired workspace has
// waiting for the operator's decision, under Needs you in /reviews, in
// /pair status, in the footer and on /pair consent. Counting reads only:
// nothing opens, takes focus, acknowledges or decides. An arrival only
// reads the counts again.

const (
	kindConsentCount     = "consent count"
	panelHeld            = "consent held"
	consentContactPrefix = "contact:"
)

// consentHeld is the held requests by contact, from the last complete read,
// and the read in flight.
type consentHeld struct {
	byContact map[string][]string // contact → held request IDs
	token     string              // the read in flight, if any
	acc       map[string][]string // what that read gathered so far
	again     bool                // a change arrived meanwhile: read once more
	show      bool                // /pair consent waits for the read
	rows      []string            // the held panel's rows, as last opened
}

// add gathers a page's requests by contact.
func (h *consentHeld) add(items []protocol.ConsentView) {
	if h.acc == nil {
		h.acc = map[string][]string{}
	}
	for _, v := range items {
		h.acc[v.Contact] = appendOnce(h.acc[v.Contact], v.ID)
	}
}

// setContact replaces one contact's held requests after its full read.
func (h *consentHeld) setContact(contact string, items []protocol.ConsentView) {
	if h.byContact == nil {
		h.byContact = map[string][]string{}
	}
	ids := make([]string, 0, len(items))
	for _, v := range items {
		ids = append(ids, v.ID)
	}
	h.byContact[contact] = ids
}

// total is every held request counted.
func (h *consentHeld) total() int {
	n := 0
	for _, ids := range h.byContact {
		n += len(ids)
	}
	return n
}

// contacts are the contacts with held requests, by key.
func (h *consentHeld) contacts() []string {
	var keys []string
	for key, ids := range h.byContact {
		if len(ids) > 0 {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// readHeld reads the counts again: one read at a time, and one more after
// it if changes kept coming.
func (m *Model) readHeld() tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityConsentPrompts) {
		return nil
	}
	if m.held.token != "" {
		m.held.again = true
		return nil
	}
	m.held.acc = nil
	return m.readHeldPage(uuid.NewString(), 0)
}

func (m *Model) readHeldPage(token string, before int64) tea.Cmd {
	m.held.token = token
	return m.request(kindConsentCount, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpConsentList,
		MessageID: uuid.NewString(), ContactConsentBefore: before}, token)
}

// heldCountResult gathers a page of the counts read; a refusal keeps the
// counts as they were.
func (m *Model) heldCountResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	h := &m.held
	if req.ids[0] != h.token {
		return nil
	}
	page := resp.ConsentPage
	if page == nil {
		h.token, h.acc = "", nil
		m.heldUnread(resp)
		return nil
	}
	h.add(page.Items)
	if page.More && page.Next > 0 {
		return m.readHeldPage(h.token, page.Next)
	}
	h.byContact, h.acc, h.token = h.acc, nil, ""
	if h.show {
		h.show = false
		m.showHeldSummary()
	}
	if h.again {
		h.again = false
		return m.readHeld()
	}
	return nil
}

// heldUnread reports a refused read when /pair consent waited for it.
func (m *Model) heldUnread(resp protocol.Message) {
	if m.held.show {
		m.held.show = false
		m.status = "held requests not read: " + refusalReason(resp)
	}
}

// heldFromPage takes the counts from a preview read that saw every held
// request at once: the first page across contacts, with nothing more.
func (m *Model) heldFromPage(r consentRead, page *protocol.ConsentPage) {
	if !r.acrossContacts() || page.More {
		return
	}
	m.held.acc = nil
	m.held.add(page.Items)
	m.held.byContact, m.held.acc = m.held.acc, nil
}

// acrossContacts: the read is the first page across every contact.
func (r consentRead) acrossContacts() bool { return r.contact == "" && r.before == 0 }

// heldSummaryCommand is /pair consent alone: read the counts, then one
// line per contact with held requests.
func (m *Model) heldSummaryCommand() tea.Cmd {
	m.held.show = true
	m.status = "reading held requests…"
	if m.held.token != "" {
		return nil // the read in flight answers it
	}
	return m.readHeld()
}

// showHeldSummary lists who has requests held; it opens no request.
func (m *Model) showHeldSummary() {
	keys := m.held.contacts()
	if len(keys) == 0 {
		m.status = "no requests are held for your decision"
		return
	}
	rows := []string{titleStyle.Render("Held for your decision") + mutedStyle.Render("  (Esc closes; nothing is decided here)")}
	for _, key := range keys {
		rows = append(rows, "  "+m.heldLine(key))
	}
	rows = append(rows, "", mutedStyle.Render("  /pair consent on NAME · off NAME · reset NAME AGENT OPERATION"))
	m.openHeldPanel(rows)
	m.status = fmt.Sprintf("%s held for your decision", plural(m.held.total(), "request"))
}

// heldLine is "NAME: N waiting — /pair consent contact:NAME".
func (m *Model) heldLine(key string) string {
	return fmt.Sprintf("%s: %d waiting — /pair consent %s", safe(m.contactTitle(contactKey(key))), len(m.held.byContact[key]),
		consentContactPrefix+m.contactToken(key))
}

// openHeldPanel shows fixed rows in the held panel.
func (m *Model) openHeldPanel(rows []string) {
	m.held.rows = rows
	m.panel = &panel{kind: panelHeld}
}

// heldNeedRows are Needs you's entries for held requests, one per contact.
func (m *Model) heldNeedRows() []string {
	var rows []string
	for _, key := range m.held.contacts() {
		rows = append(rows, m.rowMark(dotYellow)+"held  "+m.heldLine(key))
	}
	return rows
}

// heldWaiting are /pair status's rows for held requests, per contact.
func (m *Model) heldWaiting() []string {
	var rows []string
	for _, key := range m.held.contacts() {
		rows = append(rows, fmt.Sprintf("  ⇄ %s: %s held for your decision · /pair consent %s", safe(m.contactTitle(contactKey(key))),
			plural(len(m.held.byContact[key]), "request"), consentContactPrefix+m.contactToken(key)))
	}
	return rows
}

// heldReminder is the footer's part while requests are held.
func (m *Model) heldReminder() string {
	n := m.held.total()
	if n == 0 {
		return ""
	}
	return bandWarn.Render(fmt.Sprintf("%d held for you", n)) + bandStyle.Render(" · /pair consent")
}

// heldIDs are every held request's ID, for completion.
func (m *Model) heldIDs() []string {
	var ids []string
	for _, key := range m.held.contacts() {
		ids = append(ids, m.held.byContact[key]...)
	}
	return ids
}

// knownContacts are the paired workspaces this side knows: the pairing
// snapshot's and any with held requests.
func (m *Model) knownContacts() []string {
	var keys []string
	for _, v := range m.contactViews() {
		keys = appendOnce(keys, v.contact)
	}
	for _, key := range m.held.contacts() {
		keys = appendOnce(keys, key)
	}
	return keys
}

// consentName is what a command typed to name a paired workspace: its
// title, its key or a prefix of its digest.
type consentName string

// resolveConsentContact is the one paired workspace NAME names: by what
// this side calls it, its key, or at least a short key's worth of its
// digest. None, or more than one, is refused; the first match is never
// picked.
func (m *Model) resolveConsentContact(name consentName) (string, string) {
	hits := m.contactHits(name)
	switch len(hits) {
	case 0:
		return "", "no paired workspace " + safe(string(name)) + "; /pair status lists them"
	case 1:
		return hits[0], ""
	}
	return "", fmt.Sprintf("%s names %d paired workspaces; use more of its key (/pair status shows it)", safe(string(name)), len(hits))
}

// contactHits are the known contacts name names.
func (m *Model) contactHits(name consentName) []string {
	var hits []string
	for _, key := range m.knownContacts() {
		if m.namesContact(key, name) {
			hits = append(hits, key)
		}
	}
	return hits
}

// namesAny: name already names at least one contact, so completing it to
// a longer token could pick one of several.
func (m *Model) namesAny(name consentName) bool { return len(m.contactHits(name)) > 0 }

// namesContact: name is the contact's title, key or a long enough digest
// prefix.
func (m *Model) namesContact(key string, name consentName) bool {
	if name == "" {
		return false
	}
	if key == string(name) || m.contactTitle(contactKey(key)) == string(name) {
		return true
	}
	digest := protocol.KeyDigest(string(name))
	return len(digest) >= len(protocol.ShortKey(key)) && strings.HasPrefix(protocol.KeyDigest(key), digest)
}

// contactToken is how a command names the contact: its title when that is
// one word naming it alone, else the shortest digest prefix that does.
func (m *Model) contactToken(key string) string {
	if title := m.contactTitle(contactKey(key)); m.tokenNames(consentName(title), key) {
		return title
	}
	digest := protocol.KeyDigest(key)
	for n := len(protocol.ShortKey(key)); n < len(digest); n++ {
		if m.tokenNames(consentName(digest[:n]), key) {
			return digest[:n]
		}
	}
	return key
}

// tokenNames: token is one plain word that resolves to key alone.
func (m *Model) tokenNames(token consentName, key string) bool {
	if !token.plainWord() {
		return false
	}
	resolved, problem := m.resolveConsentContact(token)
	return problem == "" && resolved == key
}

// plainWord: the name is one non-empty word with nothing to escape.
func (n consentName) plainWord() bool {
	if n == "" {
		return false
	}
	return !strings.ContainsFunc(string(n), isSpace) && safe(string(n)) == string(n)
}
