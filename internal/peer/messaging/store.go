package messaging

import (
	"context"
	"errors"
	"time"
)

// Outgoing is a message to queue: to a contact, under an ID that names it,
// at a time.
type Outgoing struct {
	Contact, ID, Text string
	Now               time.Time
	// Routing, sent only at RoutingVersion: as in Message.
	From    string
	To      []string
	ReplyTo ReplyRef
	// LocalID is the local agent message this one was queued from, if any:
	// what a reply to it from the contact answers here.
	LocalID string
}

// Limit is how many messages, and how many bytes of their text, one call
// may take.
type Limit struct {
	Count, Bytes int
	// Version is the session's: before RoutingVersion, messages to or from
	// agents stay queued for a peer that can carry them. 0 is the newest.
	Version byte
}

// SessionLimit is what one session carries each way.
var SessionLimit = Limit{Count: SessionMessages, Bytes: SessionBytes}

// Row is one message as a side holds it, either direction.
type Row struct {
	Cursor      int64  // local insertion order; Page pages by it
	Contact     string // the peer's identity key
	Direction   string // DirOut or DirIn
	Message            // Text is empty once collected
	State       string
	Refusal     Refusal // the last refusal: terminal for StateRefused, transient on a queued row
	DeliveredAt *time.Time
	ReadAt      *time.Time
	Receipt     string // inbound rows read here: ReceiptPending, ReceiptConfirmed or ReceiptDropped
}

// Answer is the receiver's answer to one Message.
type Answer struct {
	Stored bool
	Refusal
}

// Store keeps both directions of every contact's messages durably. Every
// method takes the peer's identity key; Queue and Accept require a
// confirmed contact, the rest act only on that contact's own rows.
type Store interface {
	// Queue stores an outbound message with the next sequence (from 1, never
	// reused). The same ID with the same text returns the existing row; with
	// other text, ErrConflict.
	Queue(ctx context.Context, o Outgoing) (Row, error)
	// Offer is the messages still to send within the limit, oldest first; a
	// queued row past QueuedExpiry becomes StateExpired.
	Offer(ctx context.Context, contact string, now time.Time, l Limit) ([]Row, error)
	// Delivered records the peer's Stored; it never moves a read row back.
	Delivered(ctx context.Context, contact, id string, at time.Time) error
	// Refused records the peer's refusal of one message: a terminal one ends
	// the row, a transient one keeps it queued with its reason.
	Refused(ctx context.Context, contact string, r Refused) error
	// Accept decides one inbound Message and stores it when accepted: a
	// replay first, then the replay floor, then validity and quota.
	Accept(ctx context.Context, contact string, m Message, now time.Time) (Answer, error)
	// MarkRead marks the given inbound messages read, as shown here, and
	// queues one receipt for each newly read one.
	MarkRead(ctx context.Context, contact string, ids []string, at time.Time) (int, error)
	// PendingReads is up to max read receipts not yet confirmed by the peer.
	PendingReads(ctx context.Context, contact string, max int) ([]string, error)
	// ReadConfirmed records the peer's ReadStored.
	ReadConfirmed(ctx context.Context, contact, id string) error
	// PeerRead records the peer's Read of an outbound message; anything
	// else is ignored.
	PeerRead(ctx context.Context, contact, id string, at time.Time) error
	// Page is up to limit rows before the cursor (0 for the newest), newest
	// first, and whether older ones exist.
	Page(ctx context.Context, contact string, before int64, limit int) ([]Row, bool, error)
	// Collect expires, collects text, raises replay floors and drops
	// receipts, within the bounds.
	Collect(ctx context.Context, now time.Time) error
}

// RoutingStore is what a version 3 session needs besides Store: messages to
// and from agents, their recipient-specific acknowledgments, and the agents
// each side shares. A Store without it speaks at most version 2.
type RoutingStore interface {
	Store
	// PendingAgentReads is up to max agent acknowledgments still to send.
	PendingAgentReads(ctx context.Context, contact string, max int) ([]AgentRead, error)
	// AgentReadConfirmed records the peer's AgentReadStored.
	AgentReadConfirmed(ctx context.Context, contact string, r AgentRead) error
	// PeerAgentRead records the peer's AgentRead of an outbound routed
	// message; an agent outside its recipients is ErrNotRecipient.
	PeerAgentRead(ctx context.Context, contact string, r AgentRead, at time.Time) error
	// SharedAgents is the agents this side shares with the contact.
	SharedAgents(ctx context.Context, contact string) ([]string, error)
	// SetPeerDirectory replaces what the contact says it shares, a hint.
	SetPeerDirectory(ctx context.Context, contact string, agents []string, at time.Time) error
}

// ErrNotRecipient is an agent acknowledgment from an agent the message was
// not sent to.
var ErrNotRecipient = errors.New("messaging: not a recipient of that message")
