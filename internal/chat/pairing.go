package chat

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/peer"
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
	pairUsage    = "usage: /pair [open | status | cancel | accept KEY | reject KEY | redeem HOST:PORT CODE [NAME] | settle [OPERATION_ID] | share AGENT...|* | unshare AGENT...|*]"
)

// pairingView is the last pairing snapshot, its code removed.
type pairingView struct {
	snapshot *protocol.PairingSnapshot
	stale    bool // pairing changed while a quiet read was in flight
}

// pairVerbArgs is how many words each /pair verb takes, itself included.
var pairVerbArgs = map[string][]int{
	protocol.PairingOpList: {1}, protocol.PairingOpOpen: {1}, protocol.PairingOpStatus: {1},
	protocol.PairingOpCancel: {1, 2}, protocol.PairingOpAccept: {2}, protocol.PairingOpReject: {2},
	protocol.PairingOpRedeem: {3, 4}, protocol.PairingOpSettle: {1, 2},
}

// pairWords are a /pair command's words, the verb first.
type pairWords []string

// pairArity: the words a verb takes; share and unshare take agents.
func pairArity(fields pairWords) bool {
	if fields.sharesAgents() {
		return len(fields) >= 2
	}
	if review, ok := fields.reviewArity(); review {
		return ok
	}
	return slices.Contains(pairVerbArgs[fields[0]], len(fields))
}

func pairArg(a argument) error {
	fields := pairWords(strings.Fields(string(a)))
	if len(fields) > 0 && !pairArity(fields) {
		return errors.New(pairUsage)
	}
	if len(fields) > 0 && fields[0] == protocol.PairingOpRedeem {
		return redeemArg(fields)
	}
	return nil
}

// redeemArg checks a redemption before anything is sent; its error never
// repeats the code.
func redeemArg(fields pairWords) error {
	req := redeemRequest(fields)
	req.OperationID = "check"
	if req.Name == "" {
		req.Name = "workspace"
	}
	if peer.CheckJoinRequest(req) != nil {
		return errors.New("redeem: want HOST:PORT, a 4 to 18 digit code and an optional name of at most 64 bytes")
	}
	return nil
}

func redeemRequest(fields pairWords) peer.JoinRequest {
	req := peer.JoinRequest{Destination: fields[1], Code: []byte(fields[2])}
	if len(fields) == 4 {
		req.Name = fields[3]
	}
	return req
}

// pairingStatus is the status line after each operation succeeds.
var pairingStatus = map[string]string{
	protocol.PairingOpOpen:   "pairing code shown; Esc closes the panel and clears it",
	protocol.PairingOpCancel: "invitation cancelled",
	protocol.PairingOpAccept: "pairing request accepted",
	protocol.PairingOpReject: "pairing request rejected",
}

// pairCommands are the /pair words with their own command.
var pairCommands = map[string]func(*Model, pairWords) tea.Cmd{
	protocol.PairingOpRedeem: (*Model).redeemCommand,
	pairShare:                (*Model).shareCommand,
	pairUnshare:              (*Model).shareCommand,
	pairReviews:              (*Model).contactReviewCommand,
	pairReview:               (*Model).contactReviewCommand,
	pairApprove:              (*Model).contactReviewCommand,
	pairDecline:              (*Model).contactReviewCommand,
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
	fields := pairWords(strings.Fields(string(arg)))
	if len(fields) == 0 {
		fields = pairWords{protocol.PairingOpList}
	}
	if command := pairCommands[fields[0]]; command != nil {
		return command(m, fields)
	}
	if fields[0] == protocol.PairingOpSettle && !m.session.HasCapability(protocol.CapabilityPairingContacts) {
		m.status = "this broker can't settle pairings; upgrade it to use /pair settle"
		return nil
	}
	target, problem := m.pairingTarget(fields)
	if problem != "" {
		m.status = problem
		return nil
	}
	m.status = "pairing " + fields[0] + "…"
	return m.request(kindPairing, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: fields[0], PairingTarget: target}, fields[0])
}

func (m *Model) pairingTarget(fields pairWords) (string, string) {
	switch fields[0] {
	case protocol.PairingOpCancel:
		return m.invitationTarget(fields[1:])
	case protocol.PairingOpAccept, protocol.PairingOpReject:
		return m.requestTarget(fields[1])
	case protocol.PairingOpSettle:
		// The exact operation, or every redemption still settling.
		return strings.Join(fields[1:], ""), ""
	}
	return "", ""
}

// invitationTarget is the invitation to cancel: the one named, or the last
// one seen.
func (m *Model) invitationTarget(named pairWords) (string, string) {
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

// redeemCommand sends a redemption under a fresh, stable message ID. The
// code goes only into that one request; nothing here keeps it.
func (m *Model) redeemCommand(fields pairWords) tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityPairingRedeem) {
		m.status = "this broker doesn't support redeeming a pairing code; upgrade it"
		return nil
	}
	req := redeemRequest(fields)
	m.status = "redeeming at " + safe(req.Destination) + "…"
	return m.request(kindPairing, protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: uuid.NewString(), PairingOp: protocol.PairingOpRedeem,
		PairingDestination: req.Destination, PairingCode: string(req.Code), PairingName: req.Name}, protocol.PairingOpRedeem)
}

// pairingResult keeps the snapshot without its code and opens the panel; only
// the answer to open carries a code into it.
func (m *Model) pairingResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	op := req.ids[0]
	if resp.Type == protocol.TypeOperatorPairingResult && resp.PairingJoin != nil {
		m.redeemResult(*resp.PairingJoin)
	}
	if resp.Type != protocol.TypeOperatorPairingResult || resp.PairingResult == nil {
		m.pairingRefused(req, resp)
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
	m.refreshDirectory() // contact conversations follow the snapshot
	m.panel = &panel{kind: panelPairing, code: code}
	m.status = pairingStatus[op]
	if v := resp.PairingSettle; v != nil {
		m.status = fmt.Sprintf("settlement queued here: %d, already queued: %d, not queued (queue full): %d; /pair status shows progress",
			v.Queued, v.AlreadyQueued, v.NotQueued)
	}
	return nil
}

// dropPairingCode forgets a shown code, and an unsent command line that may
// hold one; the invitation itself stays open and the draft is untouched.
func (m *Model) dropPairingCode() {
	if m.panel != nil {
		m.panel.code = ""
	}
	if m.compose.cmd != nil && secretCommandLine(m.compose.cmd.input.Value()) {
		m.closeCommandLine()
	}
}

// secretCommandLine reports a command line naming a secret command.
func secretCommandLine(text string) bool {
	name, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	c, _ := lookupCommand(argument(name))
	return c.secret
}

// pairingRows are the /pair panel: the invitation, then the requests.
func (m *Model) pairingRows() []string {
	rows := []string{titleStyle.Render("Pairing") + mutedStyle.Render("  (Esc closes)"),
		mutedStyle.Render("  " + protocol.PairingPreview)}
	rows = append(rows, m.identityRow())
	rows = append(rows, m.invitationRows()...)
	rows = append(rows, m.pairingRequestRows()...)
	rows = append(rows, m.pairingJoinRows()...)
	return append(rows, mutedStyle.Render("  /pair open · /pair cancel · /pair accept KEY · /pair reject KEY · /pair settle"))
}

// identityRow is this workspace's fingerprint, for the operators to compare
// by phone, or why it is unavailable.
func (m *Model) identityRow() string {
	s := m.pairing.snapshot
	switch {
	case s == nil:
		return "  this workspace: unknown"
	case s.IdentityError != "":
		return "  this workspace's identity is unavailable: " + safe(s.IdentityError) + "; opening a code needs it"
	}
	return "  this workspace: " + safe(s.SelfIdentity)
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
		rows = append(rows, m.requestRow(r)...)
	}
	if s.RequestsTruncated {
		rows = append(rows, fmt.Sprintf("  (list is partial: %d more not shown; pending requests list first, so deciding these brings the rest into view)", s.RequestsTotal-len(s.Requests)))
	}
	return rows
}

// requestRow is one request on a line, or its state on a line of its own
// when the full identity key leaves no room for it.
func (m *Model) requestRow(r protocol.PairingRequestView) []string {
	row := fmt.Sprintf("  %s  %s  %s", safe(r.IdentityKey), safe(r.Name), r.Label())
	if ansi.StringWidth(row) <= m.width {
		return []string{row}
	}
	return []string{fmt.Sprintf("  %s  %s", safe(r.IdentityKey), safe(r.Name)), "    " + r.Label()}
}

// redeemResult reports a recorded redemption; /pair shows its progress.
func (m *Model) redeemResult(j protocol.PairingJoinView) {
	m.status = fmt.Sprintf("redemption %s at %s: %s; /pair shows its progress", safe(j.OperationID), safe(j.Destination), safe(j.State))
}

// pairingRefused reports a refusal, unless a redemption was just reported.
func (m *Model) pairingRefused(req pendingRequest, resp protocol.Message) {
	op := req.ids[0]
	if resp.Type == protocol.TypeOperatorPairingResult && resp.PairingJoin != nil {
		return
	}
	m.status = "pairing " + op + " refused: " + brokerReason(resp)
}

// pairingJoinRows are this workspace's redemptions; awaiting_acceptance is
// not yet a contact.
func (m *Model) pairingJoinRows() []string {
	s := m.pairing.snapshot
	if s == nil || s.JoinsTotal == 0 {
		return nil
	}
	rows := []string{fmt.Sprintf("  redemptions (%d)", s.JoinsTotal)}
	for _, j := range s.Joins {
		rows = append(rows, fmt.Sprintf("  %s  %s  %s", safe(j.Destination), safe(j.State), safe(j.OperationID)))
	}
	if s.JoinsTruncated {
		rows = append(rows, fmt.Sprintf("  (list is partial: %d more not shown)", s.JoinsTotal-len(s.Joins)))
	}
	return rows
}
