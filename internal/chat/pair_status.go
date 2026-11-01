package chat

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
)

// /pair status in words and times, and what this side calls a paired
// workspace: /pair name NAME in its ⇄ tab, kept here only. A name never
// addresses anything; the identity key does.

const (
	pairName        = protocol.ContactOpName
	kindContactName = "contact name"
	clearName       = "--clear"
	pairTime        = "Jan 2 15:04"
)

// when is a time as the status shows it.
func when(t time.Time) string { return t.Local().Format(pairTime) }

// nameCommand sets, or with --clear forgets, what this side calls the
// workspace whose ⇄ tab is open.
func (m *Model) nameCommand(fields pairWords) tea.Cmd {
	key := m.conv.selected
	switch {
	case !m.session.HasCapability(protocol.CapabilityContactNames):
		m.status = "this broker doesn't support naming a workspace; upgrade it"
		return nil
	case !key.isContact():
		m.status = "open the workspace's ⇄ tab, then /pair name NAME"
		return nil
	}
	name := strings.Join(fields[1:], " ")
	if name == clearName {
		name = ""
	}
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: pairName, ContactKey: key.contact,
		MessageID: uuid.NewString(), ContactText: name}
	m.status = "naming…"
	return m.request(kindContactName, msg, key.contact, name)
}

// contactNameResult takes the snapshot the name answered with, so the tab
// and the status show it at once.
func (m *Model) contactNameResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	contact, name := req.ids[0], req.ids[1]
	if resp.Type != protocol.TypeOperatorContactResult || resp.PairingResult == nil {
		m.status = "name refused: " + refusalReason(resp)
		return nil
	}
	snapshot := *resp.PairingResult
	snapshot.Invitation = withoutCode(snapshot.Invitation)
	m.pairing.snapshot = &snapshot
	m.refreshDirectory()
	if name == "" {
		c, _ := m.contact(contact)
		m.status = "name cleared; it shows as " + safe(contactDisplay(c))
		return nil
	}
	m.status = "this side calls it " + safe(name)
	return nil
}

// selfRow is this workspace: the name it pairs under and its fingerprint,
// for the operators to compare by phone, or why it is unavailable.
func (m *Model) selfRow() string {
	s := m.pairing.snapshot
	switch {
	case s == nil:
		return "  this workspace: unknown"
	case s.IdentityError != "":
		return "  this workspace's identity is unavailable: " + safe(s.IdentityError) + "; opening a code needs it"
	}
	name := shownName(s.SelfName)
	if name == "" {
		return "  this workspace: " + safe(s.SelfIdentity) + mutedStyle.Render("  (no name: "+nameHint+")")
	}
	return "  this workspace: " + safe(name) + " · " + safe(s.SelfIdentity)
}

// requestTimes are when a request came, and when its decision was
// confirmed, if it was.
func requestTimes(r protocol.PairingRequestView) string {
	times := " · requested " + when(r.FirstPairedAt)
	if r.DeliveredAt != nil {
		times += " · confirmed " + when(*r.DeliveredAt)
	}
	return times
}

// joinStates name a redemption's state for people.
var joinStates = map[string]string{
	"intent": "starting", "exchanging": "exchanging the code", "outcome_unknown": "outcome unknown; /pair settle",
	"awaiting_acceptance": "waiting for them to accept", "unverified": "not verified",
	"failed": "failed", "contact": "paired", "declined": "they declined",
}

// joinRow is one redemption: where to, how it stands and when, and which
// workspace it reached, by what this side calls it.
func (m *Model) joinRow(j protocol.PairingJoinView) string {
	state, ok := joinStates[j.State]
	if !ok {
		state = safe(j.State)
	}
	row := fmt.Sprintf("  → %s  %s · started %s · updated %s", safe(j.Destination), state, when(j.CreatedAt), when(j.UpdatedAt))
	if j.SettleError != "" {
		row += ": " + safe(j.SettleError)
	}
	if c, ok := m.contact(j.Peer); ok && j.Peer != "" {
		row += " · with " + safe(contactDisplay(c))
	}
	return row + mutedStyle.Render("  "+safe(j.OperationID))
}

// pairedRows are the paired workspaces: what this side calls each, its
// key, who dials, and since when.
func (m *Model) pairedRows() []string {
	s := m.pairing.snapshot
	switch {
	case s == nil:
		return nil
	case s.ContactsTotal == 0:
		return []string{"", titleStyle.Render("  Paired workspaces"), "  no paired workspaces yet; /pair open makes a code to share"}
	}
	rows := []string{"", titleStyle.Render(fmt.Sprintf("  Paired workspaces (%d)", s.ContactsTotal))}
	for _, c := range s.Contacts {
		rows = append(rows, pairedRow(c)...)
	}
	if s.ContactsTruncated {
		rows = append(rows, fmt.Sprintf("  (list is partial: %d more not shown)", s.ContactsTotal-len(s.Contacts)))
	}
	return rows
}

// pairedRow is a paired workspace: how it stands and what this side calls
// it, then its key, who dials and since when.
func pairedRow(c protocol.PairingContactView) []string {
	name := safe(contactDisplay(c)) + selfNameNote(c)
	dials := "waits to be dialed"
	if c.Dials {
		dials = "this side dials " + safe(c.Destination)
	}
	rows := []string{standing(c).row(name),
		fmt.Sprintf("      %s · %s · paired %s", protocol.ShortKey(c.IdentityKey), dials, when(c.EstablishedAt))}
	if pausedHere(c) {
		rows = append(rows, warnStyle.Render("      "+pausedNote))
	}
	return rows
}

// nameHint is how this workspace gets a name of its own (#277): on a
// managed install with pairtask pairing on --name, else in the broker's
// environment. The chat can't tell which install it talks to, so it says both.
const nameHint = "pairtask pairing on --name NAME, or set PAIRTASK_PAIRING_NAME"

// selfNamed: this workspace pairs under a name of its own, or isn't
// known yet to pair under none.
func (m *Model) selfNamed() bool {
	s := m.pairing.snapshot
	return s == nil || shownName(s.SelfName) != ""
}

// selfNameNote is " (they call themselves X)" when this side calls the
// contact by another name than it gives itself.
func selfNameNote(c protocol.PairingContactView) string {
	peerName := shownName(c.Name)
	if c.LocalName == "" || peerName == "" {
		return ""
	}
	if peerName == c.LocalName {
		return ""
	}
	return " (they call themselves " + safe(peerName) + ")"
}

// redeemNameHint warns a redemption without NAME when this workspace has
// no name of its own: the other side would see it unnamed.
func (m *Model) redeemNameHint(named bool) string {
	if named || m.selfNamed() {
		return ""
	}
	return "; they will see this workspace unnamed: add NAME, or " + nameHint
}
