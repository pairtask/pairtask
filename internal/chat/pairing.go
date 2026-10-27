package chat

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Operator pairing (#178): /pair opens a short-code invitation and decides
// the pairing requests it leads to. The code lives only in the pairing panel
// that the open response creates: never in the transcript, the status line or
// the state view. Closing or replacing that panel, or losing the connection,
// drops it.

const (
	kindPairing  = "pairing"
	panelPairing = "pairing"
	pairUsage    = "usage: /pair [open | status | cancel | accept KEY | reject KEY]"
)

// pairingView is the last pairing snapshot, its code removed.
type pairingView struct {
	snapshot *protocol.PairingSnapshot
}

// pairVerbArgs is how many words each /pair verb takes, itself included.
var pairVerbArgs = map[string][]int{
	protocol.PairingOpList: {1}, protocol.PairingOpOpen: {1}, protocol.PairingOpStatus: {1},
	protocol.PairingOpCancel: {1, 2}, protocol.PairingOpAccept: {2}, protocol.PairingOpReject: {2},
}

func pairArg(a argument) error {
	fields := strings.Fields(string(a))
	if len(fields) > 0 && !slices.Contains(pairVerbArgs[fields[0]], len(fields)) {
		return errors.New(pairUsage)
	}
	return nil
}

// pairingStatus is the status line after each operation succeeds.
var pairingStatus = map[string]string{
	protocol.PairingOpOpen:   "pairing code shown; Esc closes the panel and clears it",
	protocol.PairingOpCancel: "invitation cancelled",
	protocol.PairingOpAccept: "pairing request accepted",
	protocol.PairingOpReject: "pairing request rejected",
}

// pairCommand is /pair: list by default, or one operation. Accept and reject
// resolve a typed prefix to a full identity key here, never on the broker.
func (m *Model) pairCommand(arg argument) tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityPairing) {
		m.status = "this broker doesn't support pairing; upgrade it to use /pair"
		return nil
	}
	if !m.ready() {
		m.status = "not connected; /pair again once reconnected"
		return nil
	}
	fields := strings.Fields(string(arg))
	if len(fields) == 0 {
		fields = []string{protocol.PairingOpList}
	}
	target, problem := m.pairingTarget(fields)
	if problem != "" {
		m.status = problem
		return nil
	}
	m.status = "pairing " + fields[0] + "…"
	return m.request(kindPairing, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: fields[0], PairingTarget: target}, fields[0])
}

func (m *Model) pairingTarget(fields []string) (string, string) {
	switch fields[0] {
	case protocol.PairingOpCancel:
		return m.invitationTarget(fields[1:])
	case protocol.PairingOpAccept, protocol.PairingOpReject:
		return m.requestTarget(fields[1])
	}
	return "", ""
}

// invitationTarget is the invitation to cancel: the one named, or the last
// one seen.
func (m *Model) invitationTarget(named []string) (string, string) {
	if len(named) == 1 {
		return named[0], ""
	}
	if s := m.pairing.snapshot; s != nil {
		if id, err := s.InvitationID(); err == nil {
			return id, ""
		}
	}
	return "", "no open invitation known; /pair status checks"
}

// requestTarget resolves a prefix against the last listing.
func (m *Model) requestTarget(prefix string) (string, string) {
	if m.pairing.snapshot == nil {
		return "", "list the requests first: /pair"
	}
	key, err := m.pairing.snapshot.ResolveKey(prefix)
	if err != nil {
		return "", safe(prefix) + ": " + err.Error()
	}
	return key, ""
}

// pairingResult keeps the snapshot without its code and opens the panel; only
// the answer to open carries a code into it.
func (m *Model) pairingResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	op := req.ids[0]
	if resp.Type != protocol.TypeOperatorPairingResult || resp.PairingResult == nil {
		m.status = "pairing " + op + " refused: " + brokerReason(resp)
		return nil
	}
	snapshot := *resp.PairingResult
	code := ""
	if snapshot.Invitation != nil {
		invitation := *snapshot.Invitation
		code, invitation.Code = invitation.Code, ""
		snapshot.Invitation = &invitation
	}
	if op != protocol.PairingOpOpen {
		code = ""
	}
	m.pairing.snapshot = &snapshot
	m.panel = &panel{kind: panelPairing, code: code}
	m.status = pairingStatus[op]
	return nil
}

// dropPairingCode forgets a shown code; the invitation itself stays open.
func (m *Model) dropPairingCode() {
	if m.panel != nil {
		m.panel.code = ""
	}
}

// pairingRows are the /pair panel: the invitation, then the requests.
func (m *Model) pairingRows() []string {
	rows := []string{titleStyle.Render("Pairing") + mutedStyle.Render("  (Esc closes)"),
		mutedStyle.Render("  " + protocol.PairingPreview)}
	rows = append(rows, m.invitationRows()...)
	rows = append(rows, m.pairingRequestRows()...)
	return append(rows, mutedStyle.Render("  /pair open · /pair cancel · /pair accept KEY · /pair reject KEY"))
}

func (m *Model) invitationRows() []string {
	s := m.pairing.snapshot
	if s == nil || s.Invitation == nil {
		return []string{"  no open invitation; /pair open makes one"}
	}
	inv := s.Invitation
	rows := []string{fmt.Sprintf("  invitation %s: %s", safe(inv.ID), inv.Summary())}
	if m.panel.code != "" {
		return append(rows, "  code "+titleStyle.Render(safe(m.panel.code))+"  one-time; never send it in chat")
	}
	return append(rows, mutedStyle.Render("  the code is shown once, when opened; /pair cancel, then /pair open for a new one"))
}

func (m *Model) pairingRequestRows() []string {
	s := m.pairing.snapshot
	if s == nil || s.RequestsTotal == 0 {
		return []string{"  no pairing requests"}
	}
	rows := []string{fmt.Sprintf("  requests (%d)", s.RequestsTotal)}
	for _, r := range s.Requests {
		rows = append(rows, fmt.Sprintf("  %s  %s  %s", safe(r.IdentityKey), safe(r.Name), protocol.PairingStateLabel(r.State)))
	}
	if s.RequestsTruncated {
		rows = append(rows, fmt.Sprintf("  (list is partial: %d more not shown; pending requests list first, so deciding these brings the rest into view)", s.RequestsTotal-len(s.Requests)))
	}
	return rows
}
