package protocol

import (
	"slices"
	"strings"

	"github.com/pairtask/pairtask/internal/peer/messaging"
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
	// Files are the files the message names (#419): a seal binds exactly
	// these, in this order, or none.
	Files []FileRef
}

// Address names the receiving workspace in a header.
func (l PeerLink) Address() string { return "workspace:" + l.Receiver }

// FilesRecipientPrefix starts the entry that names a message's files
// among its seal's recipients: the prefix, then 128 hex digits.
const FilesRecipientPrefix = "files-sha512:"

// SealNamesFiles: a header's recipients name exactly these files: no
// files entry for a message without any, else the one entry for them in
// this order. A recipient that only starts like the entry and is short
// enough to be an agent ID is an agent, never the entry.
func SealNamesFiles(recipients []string, files []FileRef) bool {
	var named []string
	for _, r := range recipients {
		if len(r) > messaging.AgentIDBytes && strings.HasPrefix(r, FilesRecipientPrefix) {
			named = append(named, r)
		}
	}
	if len(files) == 0 {
		return len(named) == 0
	}
	digest := messaging.RefsDigest(files)
	return digest != "" && len(named) == 1 && named[0] == FilesRecipientPrefix+digest
}

// Recipients are the message's recipients as its seal names them, sorted;
// with them, for a message naming files, one entry that names exactly
// those files, so the seal covers them without another header field. That
// entry is longer than any agent ID, and Binds refuses a recipient that
// long, so no recipient list can stand for a file list or the reverse.
func (l PeerLink) Recipients() []string {
	recipients := []string{l.Address()}
	if len(l.To) == 0 {
		recipients = append(recipients, OperatorID)
	}
	recipients = append(recipients, l.To...)
	if digest := messaging.RefsDigest(l.Files); digest != "" {
		recipients = append(recipients, FilesRecipientPrefix+digest)
	}
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
	if slices.ContainsFunc(l.To, func(agent string) bool { return len(agent) > messaging.AgentIDBytes }) {
		return false
	}
	return h.Workspace == l.Contact && slices.Equal(h.Recipients, l.Recipients()) && h.InReplyTo == l.ReplyRef()
}
