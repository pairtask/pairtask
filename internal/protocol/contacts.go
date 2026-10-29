package protocol

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Contact messages (#210): the operator writes to the operator of a paired
// workspace and reads what it wrote back. Operator socket only: agents have
// no contact frames, events or capability, and a contact's text is never
// an instruction to anyone. Messages travel when the side holding the
// contact's address dials it; a send is queued at once and its state moves
// with the sessions.
const (
	TypeOperatorContact       = "operator_contact"        // operator → broker
	TypeOperatorContactResult = "operator_contact_result" // broker → operator

	// ContactOpSend queues contact_text for contact_key under message_id,
	// which names the message: a retry with the same text returns it again.
	ContactOpSend = "send"
	// ContactOpList pages one contact's messages, both directions, newest
	// first, before contact_before (0 for the newest). It marks nothing read.
	ContactOpList = "list"
	// ContactOpRead marks the contact_read IDs read as shown to the operator,
	// under message_id; only that contact's received messages change.
	ContactOpRead = "read"
	// ContactOpSync asks for a session with the contact soon; the answer is
	// the sync status, never a completed session.
	ContactOpSync = "sync"

	// ErrContactRefused is a refused contact operation; the detail names why.
	ErrContactRefused = "contact_refused"

	// CapabilityContactMessages: the broker serves operator_contact.
	CapabilityContactMessages = "contact_messages"
	// EventContactMessagesChanged tells the operator session that a
	// contact's messages changed; it carries only contact_key, so a client
	// reads them again.
	EventContactMessagesChanged = "contact_messages_changed"

	// ContactTextBytes bounds one message's text.
	ContactTextBytes = 16 << 10
	// ContactReadMax bounds the IDs one read marks.
	ContactReadMax = 64
	// ContactIDBytes bounds a message ID.
	ContactIDBytes = 128
)

// ContactMessageView is one message with a contact, as the operator sees
// it. Text is empty once collected. Delivery states are only what this
// side knows: queued means no confirmed delivery yet, expired means it is
// no longer offered, never that it wasn't delivered.
type ContactMessageView struct {
	ID            string     `json:"id"`
	Contact       string     `json:"contact"`
	Direction     string     `json:"direction"` // out or in
	Seq           uint64     `json:"seq"`       // the sender's sequence for this contact
	CreatedAt     time.Time  `json:"created_at"`
	Text          string     `json:"text"`
	State         string     `json:"state"`
	RefusalClass  string     `json:"refusal_class,omitempty"`
	RefusalReason string     `json:"refusal_reason,omitempty"`
	DeliveredAt   *time.Time `json:"delivered_at,omitempty"`
	ReadAt        *time.Time `json:"read_at,omitempty"`
	Receipt       string     `json:"receipt,omitempty"` // received messages read here: pending, confirmed or dropped
	Cursor        int64      `json:"cursor"`            // pass as contact_before for older messages
}

// ContactMessagePage is a page of one contact's messages and its sync status.
type ContactMessagePage struct {
	Contact  string               `json:"contact"`
	Messages []ContactMessageView `json:"messages"`
	More     bool                 `json:"more"`
	Sync     ContactSyncView      `json:"sync"`
}

// ContactSyncView is what this side knows of syncing with a contact.
// Requested means a session was asked for, not that one ran.
type ContactSyncView struct {
	Contact     string     `json:"contact"`
	Dials       bool       `json:"dials"` // this side opens the sessions; otherwise it waits to be dialed
	LastAttempt *time.Time `json:"last_attempt,omitempty"`
	LastSuccess *time.Time `json:"last_success,omitempty"`
	NextAt      *time.Time `json:"next_at,omitempty"`
	LastFailure string     `json:"last_failure,omitempty"`
	Failures    int        `json:"failures,omitempty"`
	Requested   bool       `json:"requested,omitempty"`
}

// contactFields are the contact request fields each operation takes;
// message_id is checked separately.
var contactFields = map[string]contactFieldSet{
	ContactOpSend: {key: true, text: true, id: true},
	ContactOpList: {key: true, before: true},
	ContactOpRead: {key: true, read: true, id: true},
	ContactOpSync: {key: true},
}

type contactFieldSet struct{ key, text, read, before, id bool }

// ValidateContactRequest checks an operator_contact frame before anything
// is looked up, audited or changed: a known operation with exactly its
// fields, a contact key, a bounded UTF-8 text, and read IDs.
func ValidateContactRequest(msg Message) error {
	allowed, ok := contactFields[msg.ContactOp]
	if !ok {
		return errors.New("contact_op must be send, list, read or sync")
	}
	if err := contactFieldsPresent(msg, allowed); err != nil {
		return err
	}
	return errors.Join(validContactKey(msg.ContactKey), validContactID(msg, allowed), validContactText(msg, allowed),
		validContactRead(msg, allowed), validContactBefore(msg))
}

// contactFieldsPresent refuses a field the operation doesn't take, any
// result field on a request, and another frame's action.
func contactFieldsPresent(msg Message, allowed contactFieldSet) error {
	if foreignAction(msg) {
		return errors.New("operator_contact carries no other frame's action or payload")
	}
	for _, f := range []struct {
		name    string
		present bool
		allowed bool
	}{
		{"contact_text", msg.ContactText != "", allowed.text},
		{"contact_read", msg.ContactRead != nil, allowed.read},
		{"contact_before", msg.ContactBefore != 0, allowed.before},
		{"contact_message", msg.ContactMessage != nil, false},
		{"contact_page", msg.ContactPage != nil, false},
		{"contact_sync", msg.ContactSync != nil, false},
		{"contact_marked", msg.ContactMarked != nil, false},
	} {
		if f.present && !f.allowed {
			return fmt.Errorf("%s is not taken by contact_op %s", f.name, msg.ContactOp)
		}
	}
	return nil
}

// foreignFields are what other operator frames act on: none may ride on a
// contact operation.
var foreignFields = []func(Message) bool{
	func(m Message) bool { return m.ReviewOp != "" || m.ReviewID != "" || m.ReviewSpec != nil },
	func(m Message) bool { return m.PairingOp != "" || m.PairingTarget != "" },
	func(m Message) bool { return m.PairingCode != "" || m.PairingDestination != "" },
	func(m Message) bool { return m.RoomOp != "" || m.Room != "" },
	func(m Message) bool { return m.To != "" || m.Group != nil || len(m.Payload) != 0 },
}

// foreignAction: a field another operator frame acts on.
func foreignAction(msg Message) bool {
	return slices.ContainsFunc(foreignFields, func(present func(Message) bool) bool { return present(msg) })
}

// validContactKey requires a full identity key, sha256: and 64 lowercase
// hex digits; a prefix or a name is resolved by the client first.
func validContactKey(key string) error {
	digest, ok := strings.CutPrefix(key, "sha256:")
	if !ok || !hexDigest(digest) {
		return errors.New("contact_key is required: a contact's full identity key, sha256: and 64 lowercase hex digits")
	}
	return nil
}

// hexDigest: 64 lowercase hex digits.
func hexDigest(s string) bool {
	return len(s) == 64 && !strings.ContainsFunc(s, notLowerHex)
}

func notLowerHex(r rune) bool { return !strings.ContainsRune("0123456789abcdef", r) }

func validContactID(msg Message, allowed contactFieldSet) error {
	if !allowed.id {
		return nil
	}
	if !validMessageIDText(msg.MessageID) {
		return fmt.Errorf("message_id names the operation: 1 to %d printable bytes", ContactIDBytes)
	}
	return nil
}

func validContactText(msg Message, allowed contactFieldSet) error {
	if !allowed.text {
		return nil
	}
	if msg.ContactText == "" || len(msg.ContactText) > ContactTextBytes || !utf8.ValidString(msg.ContactText) {
		return fmt.Errorf("contact_text is required: valid UTF-8, at most %d bytes", ContactTextBytes)
	}
	return nil
}

func validContactRead(msg Message, allowed contactFieldSet) error {
	if !allowed.read {
		return nil
	}
	if len(msg.ContactRead) == 0 || len(msg.ContactRead) > ContactReadMax {
		return fmt.Errorf("contact_read takes 1 to %d message IDs", ContactReadMax)
	}
	for _, id := range msg.ContactRead {
		if !validMessageIDText(id) {
			return fmt.Errorf("contact_read IDs are 1 to %d printable bytes", ContactIDBytes)
		}
	}
	return nil
}

func validContactBefore(msg Message) error {
	if msg.ContactBefore < 0 {
		return errors.New("contact_before is a cursor, 0 or more")
	}
	return nil
}

// validMessageIDText: 1 to ContactIDBytes of printable ASCII without spaces.
func validMessageIDText(id string) bool {
	return id != "" && len(id) <= ContactIDBytes && printableASCII(id)
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}
