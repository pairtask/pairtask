package chat

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

// /pair as a menu: Enter on a /pair line still missing the word it needs
// (bare /pair, accept or reject without a request, cancel without the
// invitation) offers the choices instead of running. Picking one only
// fills it in; the next Enter runs the line. Without a pairing snapshot the
// choices are read first, quietly; with none to offer, the line says so and
// sends nothing. /pair status lists.

// pairMenu says what each /pair word does, beside it in the picker.
var pairMenu = map[string]string{
	"open":    "show a one-time code for the other workspace",
	"status":  "invitation, requests, redemptions and contacts",
	"cancel":  "close the open invitation; paired workspaces stay",
	"accept":  "let a pairing request in",
	"reject":  "turn a pairing request away",
	"redeem":  "use the other workspace's code",
	"settle":  "finish a redemption the other side accepted",
	"share":   "let this workspace message agents here",
	"unshare": "stop it messaging agents here",
	"reviews": "what it may ask of agents here",
	"review":  "ask one of its agents for a review",
	"approve": "let one of its review requests through",
	"decline": "refuse one of its review requests",
}

// pairNeedsWord: a /pair line with these words lacks the one it needs.
func pairNeedsWord(words []string) bool {
	switch len(words) {
	case 0:
		return true
	case 1:
		return decidesRequest(words[0]) || words[0] == protocol.PairingOpCancel
	}
	return false
}

// awaitsWord handles Enter on a /pair line that lacks its word: it offers
// the choices, reads them first, or says there are none. handled is false
// for any other line, which then runs.
func (m *Model) awaitsWord() (cmd tea.Cmd, handled bool) {
	line := m.compose.cmd
	value := line.input.Value()
	l, ok := splitArgLine(strings.TrimRight(value, " ") + " ")
	if !ok || l.name != "pair" || !pairNeedsWord(l.words) || !m.pairMenuUsable() {
		return nil, false
	}
	if !strings.HasSuffix(value, " ") {
		line.input.SetValue(value + " ")
		line.input.CursorEnd()
		line.track(value, false)
	}
	line.dismissed = nil
	if len(l.words) > 0 && !m.pairing.listed {
		m.status = "reading pairing requests…"
		return m.loadPairMenu(), true
	}
	m.offerPairChoices(l)
	return nil, true
}

// loadPairMenu reads the pairing state quietly, once at a time; the answer
// fills the picker only if the line still waits for it.
func (m *Model) loadPairMenu() tea.Cmd {
	m.pairing.menuWanted = true
	if m.requested(kindPairingQuiet, protocol.PairingOpList) {
		return nil // the read in flight answers it
	}
	return m.request(kindPairingQuiet, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpList},
		protocol.PairingOpList)
}

// pairMenuLoaded refreshes a /pair line still waiting for its word after a
// quiet read; a closed line, or a dismissed picker, stays as it is.
func (m *Model) pairMenuLoaded() {
	if m.pairing.menuWanted {
		m.pairing.menuWanted, m.pairing.listed = false, true
	}
	l, ok := m.argumentLine()
	if !ok || l.name != "pair" || len(l.words) == 0 || !pairNeedsWord(l.words) || l.current != "" {
		return
	}
	if d := m.compose.cmd.dismissed; d != nil && *d == m.compose.cmd.input.Value() {
		return
	}
	m.offerPairChoices(l)
}

// offerPairChoices opens the picker for l's next word, or says there is
// nothing to choose.
func (m *Model) offerPairChoices(l argLine) {
	if len(m.pairCompletions(l)) == 0 {
		m.status = m.nothingToPick(l.words[0])
		return
	}
	m.status = ""
	m.refreshCommandPicker()
}

// nothingToPick says why a /pair word has no choices.
func (m *Model) nothingToPick(word string) string {
	if word == protocol.PairingOpCancel {
		return "no open invitation to cancel; /pair open makes one"
	}
	if s := m.pairing.snapshot; s != nil && s.RequestsTruncated {
		return "no pending request among those listed (the list is partial); type the full key"
	}
	return "no pending requests to " + word + "; /pair status lists pairing"
}

// pairMenuUsable: the broker pairs and is connected; otherwise /pair runs
// and says why it can't.
func (m *Model) pairMenuUsable() bool {
	return m.session.HasCapability(protocol.CapabilityPairing) && m.ready()
}

// openInvitation is the invitation /pair cancel would close, while open.
func (m *Model) openInvitation() []string {
	s := m.pairing.snapshot
	if s == nil || s.Invitation == nil || s.Invitation.Consumed || s.Invitation.Expired {
		return nil
	}
	return []string{s.Invitation.ID}
}

// pairLabel is a /pair picker row: a word and what it does, a request to
// decide, or the invitation to cancel.
func (m *Model) pairLabel(item string) (string, bool) {
	l, ok := m.argumentLine()
	if !ok || l.name != "pair" {
		return "", false
	}
	switch {
	case len(l.words) == 0:
		return fmt.Sprintf("  %-8s %s", safe(item), mutedStyle.Render(pairMenu[item])), true
	case len(l.words) == 1 && decidesRequest(l.words[0]):
		return m.pendingRequestLabel(item)
	case len(l.words) == 1 && l.words[0] == protocol.PairingOpCancel:
		return m.invitationLabel(item)
	}
	return "", false
}

// argumentLine is the command line as completion sees it.
func (m *Model) argumentLine() (argLine, bool) {
	if m.compose.cmd == nil {
		return argLine{}, false
	}
	return splitArgLine(m.compose.cmd.input.Value())
}

// invitationLabel is the open invitation's row: when it expires and the
// attempts it has left; its code is never shown here.
func (m *Model) invitationLabel(id string) (string, bool) {
	s := m.pairing.snapshot
	if s == nil || s.Invitation == nil || s.Invitation.ID != id {
		return "", false
	}
	v := s.Invitation
	detail := fmt.Sprintf("invitation · expires %s · %d attempts left", v.ExpiresAt.Local().Format("15:04"), v.AttemptsLeft)
	return fmt.Sprintf("  %s  %s", safe(id), mutedStyle.Render(detail)), true
}
