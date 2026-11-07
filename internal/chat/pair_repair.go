package chat

import (
	"errors"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Repairing a pairing (#276): when a paired workspace holds another
// pairing with this one, /pair invite --repair NAME opens a code, or
// /pair redeem --repair NAME CODE@HOST:PORT redeems theirs, declaring the
// identity it repairs. NAME resolves here, to exactly one workspace, and
// only its full key goes to the broker.

const (
	pairInvite = "invite"
	repairFlag = "--repair"
)

var errRepairRedeem = errors.New("redeem --repair NAME CODE@HOST:PORT: a 4 to 18 digit code at the other workspace's address")

// repairs: a repair's words, invite or redeem then --repair.
func (f pairWords) repairs() bool {
	if len(f) < 2 || f[1] != repairFlag {
		return false
	}
	return f[0] == pairInvite || f[0] == protocol.PairingOpRedeem
}

// repairArg checks a repair before anything is sent; its error never
// repeats the code.
func repairArg(fields pairWords) error {
	if fields[0] == pairInvite {
		return nil
	}
	_, _, err := repairRedeem(fields)
	return err
}

// repairShape: the words are invite, or a repair; ok, that invite
// --repair has a name and redeem --repair a name and CODE@HOST:PORT.
func (f pairWords) repairShape() (bool, bool) {
	switch {
	case f.repairs() && f[0] == pairInvite:
		return true, len(f) >= 3
	case f.repairs():
		return true, len(f) >= 4
	}
	return f[0] == pairInvite, false
}

// repairRedeem is a repairing redemption's NAME and its request.
func repairRedeem(fields pairWords) (string, peer.JoinRequest, error) {
	last := fields[len(fields)-1]
	code, destination, ok := strings.Cut(last, "@")
	req := peer.JoinRequest{Destination: destination, Code: []byte(strings.ReplaceAll(code, "-", "")), Name: "workspace", OperationID: "check"}
	if !ok || peer.CheckJoinRequest(req) != nil {
		return "", req, errRepairRedeem
	}
	req.Name, req.OperationID = "", ""
	return strings.Join(fields[2:len(fields)-1], " "), req, nil
}

// repairCommand is invite or redeem with --repair, on a broker that takes
// it, for the one workspace NAME names.
func (m *Model) repairCommand(fields pairWords) tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityPairingRepair) {
		m.status = "this broker can't repair a pairing; upgrade it"
		return nil
	}
	if fields[0] == pairInvite {
		return m.repairInvite(strings.Join(fields[2:], " "))
	}
	return m.repairJoin(fields)
}

// repairInvite opens a code that repairs the named workspace's pairing.
func (m *Model) repairInvite(name string) tea.Cmd {
	c, problem := m.namedContact(name)
	if problem != "" {
		m.status = problem
		return nil
	}
	m.status = "opening a code to repair " + safe(contactDisplay(c)) + "…"
	return m.request(kindPairing, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpOpen,
		PairingRepair: c.IdentityKey}, protocol.PairingOpOpen)
}

// repairJoin redeems their code, repairing the named workspace's pairing.
// The code goes only into that one request.
func (m *Model) repairJoin(fields pairWords) tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityPairingRedeem) {
		m.status = "this broker doesn't support redeeming a pairing code; upgrade it"
		return nil
	}
	name, req, err := repairRedeem(fields)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	c, problem := m.namedContact(name)
	if problem != "" {
		m.status = problem
		return nil
	}
	m.status = "redeeming at " + safe(req.Destination) + " to repair " + safe(contactDisplay(c)) + "…"
	return m.request(kindPairing, protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: uuid.NewString(), PairingOp: protocol.PairingOpRedeem,
		PairingDestination: req.Destination, PairingCode: string(req.Code), PairingRepair: c.IdentityKey}, protocol.PairingOpRedeem)
}

// contactNamers are tried in order, as on the command line: the first
// that names any workspace decides, and one naming several is ambiguous.
var contactNamers = []func(c protocol.PairingContactView, typed string) bool{
	func(c protocol.PairingContactView, typed string) bool { return c.IdentityKey == typed },
	startsKey,
	func(c protocol.PairingContactView, typed string) bool {
		return slices.Contains([]string{c.LocalName, shownName(c.Name), contactDisplay(c)}, typed)
	},
}

// startsKey: typed starts the contact's key digest.
func startsKey(c protocol.PairingContactView, typed string) bool {
	digest := protocol.KeyDigest(typed)
	if digest == "" {
		return false
	}
	return strings.HasPrefix(protocol.KeyDigest(c.IdentityKey), digest)
}

// namedContact is the one listed workspace typed names: by its full key,
// a prefix of it, or a name.
func (m *Model) namedContact(typed string) (protocol.PairingContactView, string) {
	s := m.pairing.snapshot
	if s == nil {
		return protocol.PairingContactView{}, "list the paired workspaces first: /pair status"
	}
	if strings.TrimSpace(typed) == "" {
		return protocol.PairingContactView{}, "name the workspace: its name or key"
	}
	for _, names := range contactNamers {
		found := namedBy(s.Contacts, typed, names)
		switch {
		case len(found) > 1:
			return protocol.PairingContactView{}, safe(typed) + " names more than one workspace; type more of its key"
		case len(found) == 1:
			return found[0], ""
		}
	}
	return protocol.PairingContactView{}, "no paired workspace is called " + safe(typed)
}

// namedBy are the contacts names matches.
func namedBy(contacts []protocol.PairingContactView, typed string, names func(protocol.PairingContactView, string) bool) []protocol.PairingContactView {
	var found []protocol.PairingContactView
	for _, c := range contacts {
		if names(c, typed) {
			found = append(found, c)
		}
	}
	return found
}
