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
	"status":  "the invitation, requests and redemptions",
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
	"name":    "what this side calls that workspace",
}

// pairNeedsWord: a /pair line with these words lacks the one it needs.
func pairNeedsWord(words []string) bool {
	switch len(words) {
	case 0:
		return true
	case 1:
		return readsSnapshot(words[0]) || words[0] == pairShare
	}
	return false
}

// sharesHere: the line is /pair share, waiting for its first agent.
func sharesHere(l argLine) bool {
	return len(l.words) == 1 && l.words[0] == pairShare
}

// readsSnapshot: the word's choices come from the pairing snapshot.
func readsSnapshot(word string) bool {
	return decidesRequest(word) || word == protocol.PairingOpCancel
}

// awaitsWord handles Enter on a /pair line that lacks its word: it offers
// the choices, reads them first, or says there are none. handled is false
// for any other line, which then runs.
func (m *Model) awaitsWord() (cmd tea.Cmd, handled bool) {
	line := m.compose.cmd
	value := line.input.Value()
	l, ok := m.pairMenuAt(value)
	if !ok {
		return nil, false
	}
	if !strings.HasSuffix(value, " ") {
		line.input.SetValue(value + " ")
		line.input.CursorEnd()
		line.track(value, false)
	}
	line.dismissed = nil
	if len(l.words) > 0 && readsSnapshot(l.words[0]) && !m.pairing.listed {
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
	if !ok || !waitsForChoice(l) {
		return
	}
	if !m.pickerDismissed() {
		m.offerPairChoices(l)
	}
}

// offerPairChoices opens the picker for l's next word, or says there is
// nothing to choose.
func (m *Model) offerPairChoices(l argLine) {
	if len(m.pairCompletions(l)) == 0 {
		m.compose.picker = nil
		m.status = m.nothingToPick(l.words[0])
		return
	}
	m.status = ""
	m.refreshCommandPicker()
}

// nothingToPick says why a /pair word has no choices.
func (m *Model) nothingToPick(word string) string {
	if word == pairShare {
		return "no local agents to share with this workspace; start one first"
	}
	if word == protocol.PairingOpCancel {
		return "no open invitation to cancel; /pair open makes one"
	}
	if s := m.pairing.snapshot; s != nil && s.RequestsTruncated {
		return "no pending request among those listed (the list is partial); type the full key"
	}
	return "no pending requests to " + word + "; /pair status lists pairing"
}

// pairMenuAt is the line as completion sees it, when it is a /pair line
// lacking its word and the menu can serve it.
func (m *Model) pairMenuAt(value string) (argLine, bool) {
	l, ok := splitArgLine(strings.TrimRight(value, " ") + " ")
	if !ok || !pairWantsWord(l) {
		return l, false
	}
	if sharesHere(l) && !m.conv.selected.isContact() {
		return l, false // outside a ⇄ tab, /pair share says where it works
	}
	return l, m.pairMenuUsable()
}

// pairWantsWord: a /pair line lacking the word it needs.
func pairWantsWord(l argLine) bool {
	return l.name == "pair" && pairNeedsWord(l.words)
}

// waitsForChoice: a /pair line after accept, reject or cancel, with
// nothing typed after it yet.
func waitsForChoice(l argLine) bool {
	if l.current != "" || len(l.words) == 0 {
		return false
	}
	return pairWantsWord(l)
}

// pickerDismissed: Esc closed the picker on the line as it is now.
func (m *Model) pickerDismissed() bool {
	line := m.compose.cmd
	return line.dismissed != nil && *line.dismissed == line.input.Value()
}

// pairMenuUsable: the broker pairs and is connected; otherwise /pair runs
// and says why it can't.
func (m *Model) pairMenuUsable() bool {
	return m.session.HasCapability(protocol.CapabilityPairing) && m.ready()
}

// openInvitation is the invitation /pair cancel would close, while open.
func (m *Model) openInvitation() []string {
	inv := m.invitation()
	if inv == nil || !stillOpen(inv) {
		return nil
	}
	return []string{inv.ID}
}

// invitation is the snapshot's invitation, if any.
func (m *Model) invitation() *protocol.InvitationView {
	if m.pairing.snapshot == nil {
		return nil
	}
	return m.pairing.snapshot.Invitation
}

// stillOpen: neither used nor expired.
func stillOpen(inv *protocol.InvitationView) bool {
	return !inv.Consumed && !inv.Expired
}

// pairLabel is a /pair picker row: a word and what it does, a request to
// decide, or the invitation to cancel.
func (m *Model) pairLabel(item string) (string, bool) {
	l, ok := m.argumentLine()
	if !ok || l.name != "pair" {
		return "", false
	}
	if len(l.words) == 0 {
		return fmt.Sprintf("  %-8s %s", safe(item), mutedStyle.Render(pairMenu[item])), true
	}
	label, found := pairWordLabels[l.words[0]]
	if !found || !labelsHere(l) {
		return "", false
	}
	return label(m, item)
}

// pairWordLabels label the choices after each /pair word that has them.
var pairWordLabels = map[string]func(*Model, string) (string, bool){
	protocol.PairingOpAccept: (*Model).pendingRequestLabel,
	protocol.PairingOpReject: (*Model).pendingRequestLabel,
	protocol.PairingOpCancel: (*Model).invitationLabel,
	pairShare:                (*Model).shareLabel,
}

// labelsHere: the word being typed is the one the /pair word's choices
// fill; share's agents fill every word after it.
func labelsHere(l argLine) bool {
	return len(l.words) == 1 || l.words[0] == pairShare
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
	v := m.invitation()
	if v == nil || v.ID != id {
		return "", false
	}
	detail := fmt.Sprintf("invitation · expires %s · %d attempts left", v.ExpiresAt.Local().Format("15:04"), v.AttemptsLeft)
	return fmt.Sprintf("  %s  %s", safe(id), mutedStyle.Render(detail)), true
}
