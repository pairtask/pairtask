// Package messaging is text between two paired workspaces (#210): the
// operator of one writes to the operator of a confirmed contact. Messages
// travel over the pairtask-message ALPN on the pinned contact identity, in
// short sessions the dialing side opens; each side stores them durably
// before it answers. Nothing here reaches an agent or carries operator
// authority: a remote message is only text from that contact.
package messaging

import (
	"errors"
	"time"
	"unicode/utf8"
)

// ALPN is the message protocol, separate from pairing's contact ALPN so an
// older inviter refuses it at the handshake and never reads it as a settle.
const ALPN = "pairtask-message/1"

// Bounds.
const (
	TextBytes         = 16 << 10 // one message's text
	OutboxMessages    = 64       // queued per contact
	OutboxBytes       = 1 << 20
	UnreadMessages    = 256 // received and unread per contact
	UnreadBytes       = 4 << 20
	SessionMessages   = 16 // per direction in one session
	SessionBytes      = 512 << 10
	Tombstones        = 100_000 // stored message identities kept per contact
	Receipts          = 1024    // pending read receipts per contact
	RetainedText      = 10_000  // read or delivered texts kept per contact
	RetainedTextBytes = 64 << 20
	IDBytes           = 128
	ImportBacklog     = 256 // messages for this side's agents not yet in their inboxes, per contact
	RefusedNotices    = 32  // review requests refused as not shared and not yet read here, per contact
	AgentIDBytes      = 128
)

// Ages.
const (
	QueuedExpiry = 7 * 24 * time.Hour  // a sender stops offering after this
	TombstoneAge = 30 * 24 * time.Hour // a receiver remembers a message this long
	ReceiptAge   = 30 * 24 * time.Hour // a pending read receipt is kept this long
)

// Refusal classes: a terminal refusal ends the message; a transient one
// leaves it queued to be offered again.
const (
	Terminal  = "terminal"
	Transient = "transient"
)

// Refusal reasons, each with its class.
const (
	ReasonInvalid   = "invalid"
	ReasonTooLarge  = "too_large"
	ReasonConflict  = "conflict"
	ReasonTooOld    = "too_old"
	ReasonInboxFull = "inbox_full"
	ReasonBusy      = "busy"
	ReasonNotShared = "not_shared" // a recipient agent isn't shared with the sender
	// ReasonPeerUpgrade marks, on this side only, a queued message to or
	// from agents held back while the peer speaks a version before routing.
	ReasonPeerUpgrade = "peer_upgrade"
)

var reasonClass = map[string]string{
	ReasonInvalid: Terminal, ReasonTooLarge: Terminal, ReasonConflict: Terminal, ReasonTooOld: Terminal,
	ReasonInboxFull: Transient, ReasonBusy: Transient, ReasonNotShared: Terminal,
}

// Refusal is why a message wasn't stored.
type Refusal struct {
	Class, Reason string
}

// Refuse is the refusal for a reason, with its class.
func Refuse(reason string) Refusal {
	return Refusal{Class: reasonClass[reason], Reason: reason}
}

// Valid reports a known reason in its own class.
func (r Refusal) Valid() bool {
	class, ok := reasonClass[r.Reason]
	return ok && class == r.Class
}

// Message states. Outbound: queued, delivered, read, expired, refused;
// inbound: received, read.
const (
	StateQueued    = "queued"    // no confirmed Stored yet; the peer may already hold it
	StateDelivered = "delivered" // Stored confirmed
	StateRead      = "read"      // the reader's operator was shown it
	StateExpired   = "expired"   // no longer offered; delivery unconfirmed, never denied
	StateRefused   = "refused"   // refused terminally
	StateReceived  = "received"  // inbound, not yet shown here
	StateRouted    = "routed"    // inbound for this side's agents, never a human read
)

// Receipt states of an inbound message read here.
const (
	ReceiptPending   = "pending"
	ReceiptConfirmed = "confirmed"
	ReceiptDropped   = "dropped" // the receipt bound was reached before the peer confirmed it
)

// Directions.
const (
	DirOut = "out"
	DirIn  = "in"
)

var (
	// ErrConflict is an ID or sequence already used with other content.
	ErrConflict = errors.New("messaging: message ID or sequence already used with other content")
	// ErrOutboxFull is a contact's outbox at its bound.
	ErrOutboxFull = errors.New("messaging: outbox full")
	// ErrUnknownContact is a contact without a confirmed pairing.
	ErrUnknownContact = errors.New("messaging: not a confirmed contact")
	// ErrInvalid is an ID or text outside the rules.
	ErrInvalid = errors.New("messaging: invalid message")
	// ErrNotShared is a message from an agent not shared with the contact.
	ErrNotShared = errors.New("messaging: agent not shared with that contact")
)

// ValidID reports an ID a message may carry: 1 to IDBytes of printable
// ASCII without spaces.
func ValidID(id string) bool {
	if id == "" || len(id) > IDBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] <= ' ' || id[i] > '~' {
			return false
		}
	}
	return true
}

// ValidText reports text a message may carry: non-empty valid UTF-8 within
// TextBytes.
func ValidText(text string) bool {
	return text != "" && len(text) <= TextBytes && utf8.ValidString(text)
}

// ValidAgent is an agent ID as the broker admits one: 1 to AgentIDBytes
// bytes, never the broadcast or the operator.
func ValidAgent(id string) bool {
	return id != "" && len(id) <= AgentIDBytes && id != "*" && id != "operator"
}

// ValidRouting: a sender that is the operator or an agent, at most MaxAgents
// distinct recipient agents, and a reply reference whose author is set
// exactly when its ID is, with a valid ID.
func ValidRouting(m Message) bool {
	if !validSender(m.From) || len(m.To) > MaxAgents {
		return false
	}
	seen := map[string]bool{}
	for _, a := range m.To {
		if !ValidAgent(a) || seen[a] {
			return false
		}
		seen[a] = true
	}
	return validReply(m.ReplyTo)
}

// validSender: the operator ("") or an agent.
func validSender(from string) bool { return from == "" || ValidAgent(from) }

func validReply(r ReplyRef) bool {
	if r.Author == ReplyAuthorNone {
		return r.ID == ""
	}
	return r.Author <= ReplyByReceiver && ValidID(r.ID)
}
