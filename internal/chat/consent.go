package chat

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Pending consent (#293): `/pair consent ID` opens one held request as a
// dedicated preview over everything else. Keys 1–5 select a choice, none
// selected at first; only Ctrl+Y confirms it. Enter, Tab, paste, the mouse
// and every global shortcut do nothing while it is open; Esc only closes it.
// Viewing it never acknowledges, reads or decides anything.

const (
	pairConsent        = "consent"
	panelConsent       = "consent"
	kindConsentList    = "consent list"
	kindConsentDecide  = "consent decide"
	consentNoSelection = -1
)

// consentChoices are the choices in key order: 1 once … 5 later.
var consentChoices = []string{protocol.ConsentOnce, protocol.ConsentSession, protocol.ConsentAlways,
	protocol.ConsentDecline, protocol.ConsentLater}

// consentChoiceLabels say what each choice does, as the prompt shows it.
var consentChoiceLabels = map[string]string{
	protocol.ConsentOnce:    "allow this request only",
	protocol.ConsentSession: "allow for this session (ends on reconnect or restart)",
	protocol.ConsentAlways:  "always allow exactly this",
	protocol.ConsentDecline: "decline, and stop asking for this",
	protocol.ConsentLater:   "decide later (stays in Needs you)",
}

// consentPrompt is the open preview: the request exactly as shown, and the
// choice selected so far. Opened for a contact, it lists that contact's
// held requests first, and a row is previewed only when picked.
type consentPrompt struct {
	view     protocol.ConsentView
	selected int
	sending  bool
	// refreshing: the request changed and is being read again; until it is,
	// nothing can be selected or confirmed on what is shown.
	refreshing bool
	// stale: the connection was refreshed since this was read; nothing can
	// be selected or confirmed on it until it is read again.
	stale bool
	list  *consentList
}

// consentList is one contact's held requests, as last read, and the row
// highlighted; previewing while one of them is shown with its choices.
type consentList struct {
	contact    string
	items      []protocol.ConsentView
	row        int
	previewing bool
}

// consentRead is one held-request read for the preview: the request it
// looks for, or every request of a contact, under the opening token.
type consentRead struct {
	id, token, contact string
	before             int64
}

func (r consentRead) ids() []string {
	return []string{r.id, r.token, r.contact, strconv.FormatInt(r.before, 10)}
}

func consentReadOf(req pendingRequest) consentRead {
	before, _ := strconv.ParseInt(req.ids[3], 10, 64)
	return consentRead{id: req.ids[0], token: req.ids[1], contact: req.ids[2], before: before}
}

// consentUnsupported: a broker without consent prompts is never sent one.
const consentUnsupported = "consent prompts need a newer broker"

const consentUsage = "/pair consent [ID | contact:NAME | on NAME | off NAME | reset NAME [AGENT [OPERATION]]]"

// consentVerbs are /pair consent's reserved first words.
var consentVerbs = map[string]func(*Model, pairWords) tea.Cmd{
	consentOn:    (*Model).consentOptCommand,
	consentOff:   (*Model).consentOptCommand,
	consentReset: (*Model).consentResetCommand,
}

// consentCommand is /pair consent: what is held, one contact's held
// requests, one request by ID, or the contact's opt-in and scopes.
func (m *Model) consentCommand(fields pairWords) tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityConsentPrompts) {
		m.status = consentUnsupported
		return nil
	}
	if len(fields) == 1 {
		return m.heldSummaryCommand()
	}
	if verb := consentVerbs[fields[1]]; verb != nil {
		return verb(m, fields)
	}
	if len(fields) != 2 {
		m.status = consentUsage
		return nil
	}
	if name, ok := strings.CutPrefix(fields[1], consentContactPrefix); ok {
		return m.consentContactCommand(name)
	}
	m.status = "reading held request… (Esc cancels)"
	return m.readConsent(consentRead{id: fields[1], token: uuid.NewString()})
}

// consentContactCommand is /pair consent contact:NAME: that contact's
// held requests, none of them previewed or chosen yet.
func (m *Model) consentContactCommand(name string) tea.Cmd {
	key, problem := m.resolveConsentContact(name)
	if problem != "" {
		m.status = problem
		return nil
	}
	m.status = "reading held requests from " + safe(m.contactTitle(contactKey(key))) + "… (Esc cancels)"
	return m.readConsent(consentRead{contact: key, token: uuid.NewString()})
}

// readConsent starts a preview read under a new opening token.
func (m *Model) readConsent(r consentRead) tea.Cmd {
	m.consentOpening, m.consentGather = r.token, nil
	return m.readConsentPage(r)
}

// readConsentPage reads one page of held requests under the read's
// opening token, so Esc still stops it.
func (m *Model) readConsentPage(r consentRead) tea.Cmd {
	return m.request(kindConsentList, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpConsentList,
		MessageID: uuid.NewString(), ContactKey: r.contact, ContactConsentBefore: r.before}, r.ids()...)
}

// consentChanged: a held request arrived, was decided or expired. The
// counts are read again; an open preview drops its selection at once and
// reads its request again, so a choice made on what was shown before can
// never be confirmed. Nothing opens on its own.
func (m *Model) consentChanged() tea.Cmd {
	c := m.consent
	if c == nil {
		return m.readHeld()
	}
	c.selected, c.sending, c.refreshing = consentNoSelection, false, true
	if l := c.list; l != nil && !l.previewing {
		return m.readConsent(consentRead{contact: l.contact, token: uuid.NewString()})
	}
	return m.readConsent(consentRead{id: c.view.ID, token: uuid.NewString()})
}

// invalidateConsent: the connection was refreshed. A read for the preview
// is dropped however late it answers, and what is shown can be neither
// chosen nor confirmed until it is read again.
func (m *Model) invalidateConsent() {
	if m.consentOpening != "" {
		m.consentOpening, m.consentGather = "", nil
		m.status = "held request not opened: the connection was refreshed; open it again"
	}
	if c := m.consent; c != nil {
		c.selected, c.refreshing, c.stale = consentNoSelection, false, true
	}
}

// cancelConsentOpening: Esc while a preview is being read means it never
// opens, however late the answer comes.
func (m *Model) cancelConsentOpening() {
	m.consentOpening, m.consentGather = "", nil
	m.status = "held request not opened"
}

// consentListResult opens the preview of the request asked for, or the
// contact's list, if the broker still holds them.
func (m *Model) consentListResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	r := consentReadOf(req)
	if r.token != m.consentOpening {
		return nil // dismissed, refreshed, or superseded by a later /pair consent
	}
	page := resp.ConsentPage
	if page == nil {
		m.closeConsent()
		m.status = "held request unavailable: " + refusalReason(resp)
		return nil
	}
	m.heldFromPage(r, page)
	if r.id == "" {
		return m.gatherContact(r, page)
	}
	return m.findConsent(r, page)
}

// findConsent shows the request looked for, or reads older pages for it.
func (m *Model) findConsent(r consentRead, page *protocol.ConsentPage) tea.Cmd {
	for _, v := range page.Items {
		if v.ID == r.id {
			m.showConsentView(v)
			return nil
		}
	}
	if page.More && page.Next > 0 {
		r.before = page.Next
		return m.readConsentPage(r) // older: keep looking
	}
	m.closeConsent() // gone: a preview of it would be stale
	m.status = "no held request " + safe(r.id) + " (decided or expired)"
	return nil
}

// gatherContact collects a contact's held requests, every page, then lists
// them.
func (m *Model) gatherContact(r consentRead, page *protocol.ConsentPage) tea.Cmd {
	m.consentGather = append(m.consentGather, page.Items...)
	if page.More && page.Next > 0 {
		r.before = page.Next
		return m.readConsentPage(r)
	}
	items := m.consentGather
	m.consentOpening, m.consentGather = "", nil
	m.held.setContact(r.contact, items)
	if len(items) == 0 {
		m.closeConsent()
		m.status = "no requests from " + safe(m.contactTitle(contactKey(r.contact))) + " are held for your decision"
		return nil
	}
	m.showConsentList(r.contact, items)
	return nil
}

// showConsentView previews one request with no choice selected.
func (m *Model) showConsentView(v protocol.ConsentView) {
	m.consentOpening = ""
	c := m.consent
	if c == nil || c.list == nil {
		c = &consentPrompt{}
		m.consent = c
	}
	c.view, c.selected, c.sending, c.refreshing, c.stale = v, consentNoSelection, false, false, false
	if c.list != nil {
		c.list.previewing = true
	}
	m.panel = &panel{kind: panelConsent}
	m.status = "choose 1–5, then Ctrl+Y to confirm; Esc closes without deciding"
}

// showConsentList lists a contact's held requests; a refresh keeps the
// highlighted request where it still is.
func (m *Model) showConsentList(contact string, items []protocol.ConsentView) {
	row := 0
	if c := m.consent; c != nil && c.list != nil && c.list.row < len(c.list.items) {
		row = max(0, slices.IndexFunc(items, func(v protocol.ConsentView) bool { return v.ID == c.list.items[c.list.row].ID }))
	}
	m.consent = &consentPrompt{selected: consentNoSelection, list: &consentList{contact: contact, items: items, row: row}}
	m.panel = &panel{kind: panelConsent}
	m.status = "↑/↓ highlight a held request · Enter previews it · Esc closes; nothing is decided here"
}

// consentKey is every key while the preview is open: nothing reaches the
// composer, the command line or a global shortcut.
func (m *Model) consentKey(msg tea.KeyPressMsg) tea.Cmd {
	if l := m.consent.list; l != nil && !l.previewing {
		m.consentListKey(msg.String())
		return nil
	}
	key := msg.String()
	if direction, ok := consentScrollKeys[key]; ok {
		m.scrollConsent(direction)
		return nil
	}
	switch {
	case key == "esc":
		m.backFromPreview()
	case key == "ctrl+y":
		return m.confirmConsent()
	case consentChoiceKey(key):
		m.selectConsent(int(key[0] - '1'))
	}
	return nil
}

// consentScrollKeys move the preview, so every target and scope can be
// read before deciding; a page for PgUp/PgDn, a line for the arrows.
var consentScrollKeys = map[string]int{"pgup": -2, "pgdown": 2, "up": -1, "down": 1}

func consentChoiceKey(key string) bool { return len(key) == 1 && key >= "1" && key <= "5" }

// selectConsent selects a choice, unless what is shown is being replaced.
func (m *Model) selectConsent(i int) {
	c := m.consent
	if c.sending || c.refreshing || c.stale {
		return
	}
	c.selected = i
}

// scrollConsent moves the preview by a line (±1) or a page (±2); the
// conversation and the draft stay where they are.
func (m *Model) scrollConsent(step int) {
	if m.panel == nil {
		return
	}
	if step == -2 || step == 2 {
		m.panel.scroll(step/2, m.layout().transcript)
		return
	}
	m.panel.offset = max(0, min(m.panel.offset+step, max(0, m.panel.rows-1)))
}

// confirmConsent sends the selected choice for the request exactly as shown,
// with this connection's epoch and generation as a stale guard.
func (m *Model) confirmConsent() tea.Cmd {
	c := m.consent
	if c.stale {
		m.status = consentStaleNote
		return nil
	}
	if c.refreshing {
		m.status = "the request changed; wait for it to be shown again, then choose"
		return nil
	}
	if c.selected == consentNoSelection || c.sending {
		m.status = "choose 1–5 first; Ctrl+Y confirms the selected choice"
		return nil
	}
	c.sending = true
	choice := consentChoices[c.selected]
	m.status = "deciding " + safe(c.view.ID) + ": " + choice + "…"
	decision := &protocol.ConsentDecideRequest{ConsentID: c.view.ID, Digest: c.view.Digest, Revision: c.view.Revision,
		Choice: choice, Epoch: m.session.Epoch, Gen: m.session.Gen}
	return m.request(kindConsentDecide, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpConsentDecide,
		MessageID: uuid.NewString(), ContactKey: c.view.Contact, ConsentDecide: decision}, c.view.ID)
}

// consentDecideResult reports the decision and closes the preview.
func (m *Model) consentDecideResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	r := resp.ConsentResult
	if r == nil {
		m.status = "decision not recorded: " + refusalReason(resp)
		if m.consent != nil {
			m.consent.sending = false
		}
		return nil
	}
	m.status = consentResultLine(r)
	if m.consent != nil && m.consent.view.ID == req.ids[0] {
		m.closeConsent()
	}
	return nil
}

func consentResultLine(r *protocol.ConsentResult) string {
	line := safe(r.ConsentID) + ": " + safe(r.State)
	if r.Reason != "" {
		line += " (" + safe(r.Reason) + ")"
	}
	return line
}

// closeConsent closes the preview; the draft and focus are as they were.
func (m *Model) closeConsent() {
	m.consent, m.consentOpening = nil, ""
	if m.panel != nil && m.panel.kind == panelConsent {
		m.panel = nil
	}
}

// consentRows are the preview: who, to whom, what, and the choices.
func (m *Model) consentRows() []string {
	c := m.consent
	if c == nil {
		return nil
	}
	if c.list != nil && !c.list.previewing {
		return m.consentListRows()
	}
	v := c.view
	rows := []string{titleStyle.Render("Held request "+safe(v.ID)) + mutedStyle.Render("  (Esc closes without deciding)"),
		"  from workspace " + safe(v.WorkspaceKey) + labelNote(v.Label),
		"  sender it claims: " + orNone(v.ClaimedFrom) + mutedStyle.Render(" (data, not identity)")}
	for _, t := range v.Targets {
		rows = append(rows, "  to "+safe(t.Agent)+": "+safe(strings.Join(t.Operations, ", ")))
	}
	rows = append(rows, consentReviewRows(v.Review)...)
	if v.Text != "" {
		rows = append(rows, "  text:")
		for _, line := range strings.Split(v.Text, "\n") {
			rows = append(rows, "    "+safeText(line))
		}
	}
	rows = append(rows, mutedStyle.Render(fmt.Sprintf("  received %s · expires %s · revision %d", v.ReceivedAt.Format(time.RFC3339),
		v.ExpiresAt.Format(time.RFC3339), v.Revision)), "")
	rows = append(rows, m.consentNoteRows()...)
	return append(rows, m.consentChoiceRows()...)
}

func labelNote(label string) string {
	if label == "" {
		return ""
	}
	return mutedStyle.Render(" (you call it " + safe(label) + ")")
}

func orNone(s string) string {
	if s == "" {
		return "their operator"
	}
	return safe(s)
}

// consentReviewRows show a review request's pins and policy, as data.
func consentReviewRows(r *protocol.ReviewAsk) []string {
	if r == nil {
		return nil
	}
	return []string{"  review: " + safe(r.Repository) + " " + safe(r.Change) + " (" + safe(r.Forge) + ")",
		"    base " + safe(r.Base) + " · head " + safe(r.Head),
		fmt.Sprintf("    scope %s · publish %t · formal %t", orNone(r.Scope), r.Publish, r.Formal)}
}

// consentChoiceRows list the choices, the selected one marked, and how to
// confirm.
func (m *Model) consentChoiceRows() []string {
	var rows []string
	for i, choice := range consentChoices {
		mark := "  "
		if i == m.consent.selected {
			mark = "▸ "
		}
		rows = append(rows, fmt.Sprintf("%s%d %s: %s", mark, i+1, choice, consentChoiceLabels[choice]))
	}
	return append(rows, mutedStyle.Render("  1–5 select · Ctrl+Y confirms the selected choice · Enter and paste do nothing · Esc closes"))
}

// wheelStep is a wheel turn as a preview line: up -1, down 1.
func wheelStep(msg tea.MouseWheelMsg) int {
	switch msg.Button {
	case tea.MouseWheelUp:
		return -1
	case tea.MouseWheelDown:
		return 1
	}
	return 0
}
