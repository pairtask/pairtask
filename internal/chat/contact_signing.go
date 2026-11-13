package chat

import (
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
)

// Signed messages with paired workspaces (#115): the chat checks each
// received message's seal itself, against the keys this workspace pinned
// (operator_identity status), and says on the row who it proves sent it.
// The broker's word is never shown as a verdict.

// signingView is what the chat verifies a contact's seals against, from
// its newest page: this workspace's key and the roots pinned here.
type signingView struct {
	own   string
	roots []protocol.SigningRoot
}

// signedRow is a row's signing line: who its seal proves sent it, or that
// it is unsigned, couldn't travel, or doesn't hold; "" when there's
// nothing to say (no roots yet, or the text was collected).
func (m *Model) signedRow(v protocol.ContactMessageView) string {
	if v.Direction == "out" {
		if v.SealDowngraded {
			return "· sent unsigned: they run a version without signatures"
		}
		return ""
	}
	t := m.contacts.threads[v.Contact]
	if !checkable(t, v) {
		return ""
	}
	return verdictLine(verifyContactRow(v, t.signing))
}

// checkable: the row's text is still held and its thread knows this
// workspace's key to check the seal against.
func checkable(t *contactThread, v protocol.ContactMessageView) bool {
	return t != nil && t.signing.own != "" && v.Text != ""
}

// verifyContactRow is a received row's verdict as this chat sees it.
func verifyContactRow(v protocol.ContactMessageView, view signingView) signing.Result {
	if v.Seal == "" {
		return signing.Result{State: signing.Unsigned}
	}
	seal, err := protocol.DecodeSeal(v.Seal)
	if err != nil {
		return signing.Result{State: signing.Invalid, Reason: "unreadable seal"}
	}
	result := signing.Verify(&seal, protocol.RootsOf(view.roots), time.Now())
	kind, id := signing.SenderAgent, v.From
	if v.From == "" {
		kind, id = signing.SenderOperator, protocol.OperatorID
	}
	link := protocol.PeerLink{Contact: v.Contact, Receiver: view.own, To: v.To, ReplyID: v.ReplyID, ReplyAuthor: replyAuthors[v.ReplySide]}
	frame := signing.Frame{MessageID: v.ID, SenderKind: kind, SenderID: id, Text: v.Text, Addresses: []string{link.Address()}}
	if result.State != signing.Invalid && !signsRow(seal, frame, link) {
		return signing.Result{State: signing.Invalid, Reason: "the seal signs another message"}
	}
	return result
}

// replyAuthors are a view's reply sides as the wire's author byte.
var replyAuthors = map[string]byte{protocol.ReplyBySender: 1, protocol.ReplyByReceiver: 2}

// signsRow: the seal signs this row's message, from its contact, to its
// recipients, answering its parent.
func signsRow(seal signing.Seal, frame signing.Frame, link protocol.PeerLink) bool {
	return seal.Matches(frame) == nil && link.Binds(seal.Header)
}

// verdictLine says a verdict on a row.
func verdictLine(r signing.Result) string {
	switch {
	case r.State == signing.Unsigned:
		return "· unsigned"
	case r.State == signing.Invalid:
		return "· signature doesn't hold: " + safe(r.Reason)
	case r.State == signing.Unverified:
		return "· signed with a key not accepted here: /pair accept-key once you've compared it"
	case r.Signer != nil && r.Signer.Basis == signing.BasisPerson:
		return "· signed by their operator"
	}
	return "· signed: their workspace attests the agent"
}
