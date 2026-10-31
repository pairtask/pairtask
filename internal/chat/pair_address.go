package chat

import (
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

// /pair address HOST:PORT in a ⇄ tab: the paired workspace came back at
// another address. The broker dials it there and moves the contact only
// once it proves the same identity, so nothing is paired anew; the one
// answer comes after that check, and only then does anything change here.

const (
	pairAddress        = protocol.ContactOpAddress
	kindContactAddress = "contact address"
	addressUsage       = "usage: /pair address HOST:PORT, with a port from 1 to 65535"
)

// addressCommand asks the broker to check and take a new address for the
// ⇄ tab's workspace, when this side is the one that dials it.
func (m *Model) addressCommand(fields pairWords) tea.Cmd {
	key := m.conv.selected
	if problem := m.addressChangeProblem(key, fields); problem != "" {
		m.status = problem
		return nil
	}
	msg := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: pairAddress, ContactKey: key.contact,
		MessageID: uuid.NewString(), ContactText: fields[1]}
	m.status = "checking that " + safe(fields[1]) + " is them…"
	return m.request(kindContactAddress, msg, key.contact)
}

// addressChangeProblem is why the line can't be sent, or "".
func (m *Model) addressChangeProblem(key viewKey, fields pairWords) string {
	switch {
	case !key.isContact():
		return "open the workspace's ⇄ tab, then /pair address HOST:PORT"
	case !m.session.HasCapability(protocol.CapabilityContactAddress):
		return "this broker doesn't support changing a workspace's address; upgrade it"
	case !addressLine(fields):
		return addressUsage
	case !m.dialsContact(key.contact):
		return "this side doesn't dial them; they update your address on their side, with /pair address there"
	}
	return ""
}

// addressLine: /pair address and one valid HOST:PORT.
func addressLine(fields pairWords) bool {
	return len(fields) == 2 && peer.CheckDestination(fields[1]) == nil
}

// dialsContact: the snapshot lists the contact, and this side dials it.
func (m *Model) dialsContact(contact string) bool {
	c, ok := m.contact(contact)
	return ok && c.Dials
}

// contactAddressResult takes the snapshot the change answered with and
// says where messages go now, as stored, not what was asked for.
func (m *Model) contactAddressResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeOperatorContactResult || resp.PairingResult == nil {
		m.status = addressFailure(resp)
		return nil
	}
	snapshot := *resp.PairingResult
	snapshot.Invitation = withoutCode(snapshot.Invitation)
	m.keepSnapshot(&snapshot)
	m.refreshDirectory()
	c, ok := m.contact(req.ids[0])
	if !ok {
		m.status = "address verified and updated; this snapshot doesn't list the workspace, so its current address isn't shown"
		return nil
	}
	m.status = "address verified and updated; messages go to " + safe(c.Destination) + " from the next sync"
	return nil
}

// addressRefusals are the answers that say nothing was stored: the broker
// refused the change, or the request itself.
var addressRefusals = []string{protocol.ErrContactRefused, protocol.ErrMalformedFrame}

// addressFailure says what an answer other than the snapshot means: a
// refusal changed nothing; anything else may have come after the address
// was stored, so the outcome is unconfirmed.
func addressFailure(resp protocol.Message) string {
	if resp.Type == protocol.TypeError && slices.Contains(addressRefusals, resp.Code) {
		return "address not changed: " + refusalReason(resp)
	}
	return "address change outcome unconfirmed: " + refusalReason(resp) + "; /pair status shows the stored address"
}
