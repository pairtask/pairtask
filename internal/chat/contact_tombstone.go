package chat

import (
	"fmt"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// An unpaired workspace (#276) keeps its ⇄ tab, read-only, after a
// restart too: its history, what never left and local reads stay, and a
// draft is kept, but nothing is sent, acknowledged, shared or changed.
// Its card and /pair status say how each side stands, in words that say
// what to do.

const (
	unpairedRefusal = "unpaired: history kept, read-only; pairing again needs a fresh code"
	unpairedReason  = "unpaired" // why a message expired unsent
)

// unpairedHere: this side's operator unpaired the contact.
func unpairedHere(c protocol.PairingContactView) bool {
	return c.UnpairedAt != nil
}

// unpairedContact: a listed contact this side unpaired.
func (m *Model) unpairedContact(fingerprint string) bool {
	c, ok := m.contact(fingerprint)
	return ok && unpairedHere(c)
}

// historyContact: a contact whose messages are listed and read here:
// confirmed on both sides, or unpaired with its history kept.
func (m *Model) historyContact(fingerprint string) bool {
	c, ok := m.contact(fingerprint)
	if !ok {
		return false
	}
	return c.Confirmed || unpairedHere(c)
}

// unpairedNote is "unpaired by you DATE · history kept".
func unpairedNote(c protocol.PairingContactView) string {
	return "unpaired by you " + when(*c.UnpairedAt) + " · history kept"
}

// tabMutations are the /pair words that change the open ⇄ tab's workspace.
var tabMutations = map[string]bool{pairShare: true, pairUnshare: true, pairName: true, pairAddress: true, pairPause: true,
	pairResume: true, pairReviews: true, pairApprove: true, pairDecline: true}

// tombstoneRefused refuses a change to the open tab's workspace once it
// is unpaired, saying so.
func (m *Model) tombstoneRefused(verb string) bool {
	if !tabMutations[verb] {
		return false
	}
	return m.refuseUnpaired(m.conv.selected.contact)
}

// refuseUnpaired says why nothing goes to an unpaired contact.
func (m *Model) refuseUnpaired(contact string) bool {
	if !m.unpairedContact(contact) {
		return false
	}
	m.status = unpairedRefusal
	return true
}

// peerStates, noticeStates and failureStates say how the other side
// stands, from its notices and this side's syncs.
var (
	peerStates    = map[string]string{"paused": "paused by them", "unpaired": "unpaired by them"}
	noticeStates  = map[string]string{"pending": "notice pending: they learn when they next connect", "undelivered": "notice undelivered"}
	failureStates = map[string]string{"other_pairing": "holds another pairing: /pair invite --repair", "unsupported": "unsupported: the other side must update"}
)

// standingLabels are those words for one contact, in that order.
func standingLabels(c protocol.PairingContactView) []string {
	var labels []string
	add := func(label string) {
		if label != "" {
			labels = append(labels, label)
		}
	}
	add(peerStates[c.PeerState])
	if c.PeerStateAvailable == "unavailable" {
		add("their state: unavailable (older version)")
	}
	add(noticeLabel(c))
	if c.Summary != nil {
		add(failureStates[c.Summary.Sync.LastFailure])
	}
	return labels
}

// noticeLabel is the notice's state, with its attempts once undelivered.
func noticeLabel(c protocol.PairingContactView) string {
	label := noticeStates[c.Notice]
	if c.Notice == "undelivered" && c.NoticeAttempts > 0 {
		label += fmt.Sprintf(" after %d attempts", c.NoticeAttempts)
	}
	return label
}

// standingLine is the labels on one line, or "".
func standingLine(c protocol.PairingContactView) string {
	return strings.Join(standingLabels(c), " · ")
}

// kindNotes say what a request or join is (#276).
var kindNotes = map[string]string{"repair": "repair", "address_move": "address move"}

// kindNote is " · re-pair", " · repair" or " · address move", or "".
func kindNote(kind string, repairs bool) string {
	if kind == "pair" && repairs {
		return " · re-pair"
	}
	if note := kindNotes[kind]; note != "" {
		return " · " + note
	}
	return ""
}

// unpairedTab is " (unpaired)" on an unpaired contact's tab, or "".
func (m *Model) unpairedTab(contact string) string {
	if m.unpairedContact(contact) {
		return " (unpaired)"
	}
	return ""
}

// expiredUnpaired: a message the unpair expired before it left.
func expiredUnpaired(v protocol.ContactMessageView) bool {
	return v.State == "expired" && v.RefusalReason == unpairedReason
}

// epochBreaks: the thread spans more than one pairing, so each pairing's
// messages are headed by it.
func epochBreaks(thread []protocol.ContactMessageView) bool {
	for _, v := range thread {
		if v.PairingEpoch != thread[0].PairingEpoch {
			return true
		}
	}
	return false
}

// newEpoch: the i-th message is the first of its pairing in the thread.
func newEpoch(thread []protocol.ContactMessageView, i int) bool {
	return i == 0 || thread[i].PairingEpoch != thread[i-1].PairingEpoch
}

// epochLabel names a pairing in the history.
func epochLabel(epoch int64) string {
	if epoch == 0 {
		return "before pairings were numbered"
	}
	return fmt.Sprintf("pairing %d", epoch)
}
