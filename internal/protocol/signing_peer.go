package protocol

import (
	"slices"

	"github.com/pairtask/pairtask/internal/signing"
)

// Signed messages across a pairing link (#115). A message's recipients, as
// its seal names them, are the receiving workspace and its recipient
// agents, or its operator; a reply names its parent with the side that
// wrote it.

const (
	// ContactOpAcceptKey accepts a contact's person key, one it signed
	// with here, as the operator's approval of it.
	ContactOpAcceptKey = "accept_key"
	// ContactOpRequireSeals sets whether this side refuses a contact's
	// unsigned messages: contact_text "on" or "off".
	ContactOpRequireSeals = "require_seals"
)

// Reply authors as the wire names them (messaging.ReplyRef's Author).
const (
	replyBySender   = 1
	replyByReceiver = 2
)

// PeerLink is a cross-link message as it travels, beside its seal: the
// workspace it came from, the one receiving it, its recipient agents (none
// for the operator) and the parent it answers with that parent's author.
type PeerLink struct {
	Contact, Receiver string
	To                []string
	ReplyID           string
	ReplyAuthor       byte
}

// Address names the receiving workspace in a header.
func (l PeerLink) Address() string { return "workspace:" + l.Receiver }

// Recipients are the message's recipients as its seal names them, sorted.
func (l PeerLink) Recipients() []string {
	recipients := []string{l.Address()}
	if len(l.To) == 0 {
		recipients = append(recipients, OperatorID)
	}
	recipients = append(recipients, l.To...)
	slices.Sort(recipients)
	return slices.Compact(recipients)
}

// ReplyRef is the parent as a header names it: "sender:<id>" for one the
// reply's sender wrote, "receiver:<id>" for one the receiver wrote, ""
// for none.
func (l PeerLink) ReplyRef() string {
	switch {
	case l.ReplyID == "":
		return ""
	case l.ReplyAuthor == replyBySender:
		return "sender:" + l.ReplyID
	case l.ReplyAuthor == replyByReceiver:
		return "receiver:" + l.ReplyID
	}
	return "unknown:" + l.ReplyID
}

// Binds: the header was signed by the contact, for exactly these
// recipients, answering exactly this parent on this side.
func (l PeerLink) Binds(h signing.Header) bool {
	return h.Workspace == l.Contact && slices.Equal(h.Recipients, l.Recipients()) && h.InReplyTo == l.ReplyRef()
}
