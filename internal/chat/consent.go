package chat

import (
	"fmt"
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
// choice selected so far.
type consentPrompt struct {
	view     protocol.ConsentView
	selected int
	sending  bool
	// refreshing: the request changed and is being read again; until it is,
	// nothing can be selected or confirmed on what is shown.
	refreshing bool
}

// consentCommand is /pair consent ID: read that held request, then preview it.
func (m *Model) consentCommand(fields pairWords) tea.Cmd {
	switch {
	case !m.session.HasCapability(protocol.CapabilityConsentPrompts):
		m.status = "this broker doesn't hold requests for your decision; upgrade it"
		return nil
	case len(fields) != 2:
		m.status = "/pair consent ID: the held request to decide (see Needs you)"
		return nil
	}
	m.status = "reading held request… (Esc cancels)"
	m.consentOpening = uuid.NewString()
	return m.request(kindConsentList, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpConsentList,
		MessageID: m.consentOpening}, fields[1], m.consentOpening)
}

// consentChanged: a held request arrived, was decided or expired. An open
// preview drops its selection at once and reads its request again, so a
// choice made on what was shown before can never be confirmed; nothing
// opens on its own.
func (m *Model) consentChanged() tea.Cmd {
	c := m.consent
	if c == nil {
		return nil
	}
	c.selected, c.sending, c.refreshing = consentNoSelection, false, true
	m.consentOpening = uuid.NewString()
	return m.request(kindConsentList, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpConsentList,
		MessageID: m.consentOpening}, c.view.ID, m.consentOpening)
}

// readConsentPage reads the page before cursor while looking for id, under
// the same opening token, so Esc still stops it.
func (m *Model) readConsentPage(id string, before int64) tea.Cmd {
	return m.request(kindConsentList, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpConsentList,
		MessageID: uuid.NewString(), ContactConsentBefore: before}, id, m.consentOpening)
}

// cancelConsentOpening: Esc while a preview is being read means it never
// opens, however late the answer comes.
func (m *Model) cancelConsentOpening() {
	m.consentOpening = ""
	m.status = "held request not opened"
}

// consentListResult opens the preview of the request asked for, if the
// broker still holds it.
func (m *Model) consentListResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	if req.ids[1] != m.consentOpening {
		return nil // dismissed, or superseded by a later /pair consent
	}
	if resp.ConsentPage == nil {
		m.closeConsent()
		m.status = "held request unavailable: " + refusalReason(resp)
		return nil
	}
	for _, v := range resp.ConsentPage.Items {
		if v.ID == req.ids[0] {
			m.consentOpening = ""
			m.consent = &consentPrompt{view: v, selected: consentNoSelection}
			m.panel = &panel{kind: panelConsent}
			m.status = "choose 1–5, then Ctrl+Y to confirm; Esc closes without deciding"
			return nil
		}
	}
	if page := resp.ConsentPage; page.More && page.Next > 0 {
		return m.readConsentPage(req.ids[0], page.Next) // older: keep looking
	}
	m.closeConsent() // gone: a preview of it would be stale
	m.status = "no held request " + safe(req.ids[0]) + " (decided or expired)"
	return nil
}

// consentKey is every key while the preview is open: nothing reaches the
// composer, the command line or a global shortcut.
func (m *Model) consentKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if direction, ok := consentScrollKeys[key]; ok {
		m.scrollConsent(direction)
		return nil
	}
	switch {
	case key == "esc":
		m.closeConsent()
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
	if c.sending || c.refreshing {
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
