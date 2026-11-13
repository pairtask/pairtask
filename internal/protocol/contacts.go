package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pairtask/pairtask/internal/peer"
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
	// ContactOpShare lets the contact message the contact_agents of this
	// workspace (#222), under message_id; "*" alone is every agent
	// registered now, not one registering later. The answer is the newest
	// page, which lists what is shared.
	ContactOpShare = "share"
	// ContactOpUnshare stops it for contact_agents, "*" alone for all:
	// messages staged for them are withheld, and their queued ones refused.
	ContactOpUnshare = "unshare"
	// ContactOpReviewGrant sets what the contact may ask of contact_agents
	// here (#235): contact_review_grant's mode ask, auto or off, and
	// whether a verdict may be published, and formally.
	ContactOpReviewGrant = "review_grant"
	// ContactOpReviewAsk asks the one agent in contact_to for the review in
	// review_ask, under message_id; contact_text is an optional note.
	ContactOpReviewAsk = "review_ask"
	// ContactOpReviewDecide approves or declines (contact_approve) the
	// contact's review request with the one ID in contact_read, while it
	// waits for this side's operator.
	ContactOpReviewDecide = "review_decide"
	// CapabilityContactReviews: the broker serves the three review ops.
	CapabilityContactReviews = "contact_reviews"

	// ContactOpName sets what this side calls the contact, under
	// message_id: contact_text, or none when it is empty. The answer is the
	// pairing snapshot.
	ContactOpName = "name"
	// CapabilityContactNames: the broker serves name.
	CapabilityContactNames = "contact_names"

	// ContactOpAddress moves the contact to the HOST:PORT in contact_text,
	// under message_id, once a probe there proved the same identity; only
	// the side that dials it may. The one answer comes after the probe:
	// the pairing snapshot, or why not.
	ContactOpAddress = "address"
	// CapabilityContactAddress: the broker serves address.
	CapabilityContactAddress = "contact_address"

	// ContactOpPause blocks new traffic with the contact, both ways, under
	// message_id, keeping the contact, its grants and its history (#276);
	// ContactOpResume starts it again. Pausing a paused contact, or
	// resuming an active one, changes nothing. The answer is the pairing
	// snapshot.
	ContactOpPause  = "pause"
	ContactOpResume = "resume"
	// CapabilityContactPause: the broker serves pause and resume.
	CapabilityContactPause = "contact_pause"

	// ContactOpAck records the operator's own acknowledgment of the
	// contact's messages (#287), under message_id: the IDs in contact_ack,
	// or every message up to the cursor contact_ack_upto, never both. It is
	// private: the contact is told nothing, and a message's read state and
	// receipt don't change.
	ContactOpAck = "ack"
	// CapabilityContactAcks: the broker serves ack and the views carry
	// acknowledgments.
	CapabilityContactAcks = "contact_acks"
	// ErrContactPaused refuses a send to a paused contact.
	ErrContactPaused = "contact_paused"
	// ErrContactUnpairedByThem refuses a send to a contact the other side
	// unpaired (#276): unpair here or pair again.
	ErrContactUnpairedByThem = "contact_unpaired_by_them"

	// CapabilityContactRouting: the broker serves share and unshare, a send
	// to the contact's agents (contact_to), and routing in the views.
	CapabilityContactRouting = "contact_routing"
	// ContactAgentsMax bounds the agents one operation names.
	ContactAgentsMax = 64

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
	// Routing (#222): the agent that sent it, "" for an operator; the
	// agents it is to, none for the operator; the message it answers on
	// its sender's side ("by_sender") or the other ("by_receiver"); and,
	// for one sent to the contact's agents, those that acknowledged it.
	From      string   `json:"from,omitempty"`
	To        []string `json:"to,omitempty"`
	ReplyID   string   `json:"reply_id,omitempty"`
	ReplySide string   `json:"reply_side,omitempty"`
	AgentAcks []string `json:"agent_acks,omitempty"`
	// Signing (#115): the sender's seal as stored (base64), whether an
	// outbound one couldn't travel because the contact predates seals, and
	// the operator client's own verdict on an inbound one (never the
	// broker's).
	Seal           string        `json:"seal,omitempty"`
	SealDowngraded bool          `json:"seal_downgraded,omitempty"`
	Verification   *Verification `json:"verification,omitempty"`
	// A review request (#235): what it asks, and for one the contact sent
	// here, where it is: awaiting this side's operator, pending dispatch,
	// imported as ReviewID, or withheld with ReviewReason.
	ReviewAsk    *ReviewAsk `json:"review_ask,omitempty"`
	ReviewState  string     `json:"review_state,omitempty"`
	ReviewID     string     `json:"review_id,omitempty"`
	ReviewReason string     `json:"review_reason,omitempty"`
	// ReviewOutcome is where a review request this side sent stands.
	ReviewOutcome *ReviewOutcomeView `json:"review_outcome,omitempty"`
	// AckState is the operator's own acknowledgment of a received message
	// (#287): before tracking, needs_ack, acked or expired; "" for a sent
	// or agent-routed one. Only a broker serving contact_acks sets it.
	AckState string     `json:"ack_state,omitempty"`
	AckedAt  *time.Time `json:"acked_at,omitempty"`
	// Consent is where a message the contact held for its operator stands
	// (#293): held, allowed, declined, expired, withheld or refused, with
	// why it ended; "" for one never held. One line per request.
	Consent       string `json:"consent,omitempty"`
	ConsentReason string `json:"consent_reason,omitempty"`
	// Here and OperatorHere are who a sent message also went to on this
	// side (#327): the tagged agents, canonical, and whether this side's
	// operator, as stored when it was sent. Only a broker serving
	// paired_mentions sets them.
	Here         []string `json:"here,omitempty"`
	OperatorHere bool     `json:"operator_here,omitempty"`
	// PairingEpoch is the pairing with the contact the message came under
	// (#276); set only for a client at PairingHistoryVersion, 0 otherwise.
	PairingEpoch int64 `json:"pairing_epoch,omitempty"`
}

// ContactHeldForUpgrade is the refusal_reason of a queued message to or
// from agents held back while the contact runs a pairtask that can't carry
// it.
const ContactHeldForUpgrade = "peer_upgrade"

// Reply sides of a ContactMessageView.
const (
	ReplyBySender   = "by_sender"
	ReplyByReceiver = "by_receiver"
)

// ContactMessagePage is a page of one contact's messages and its sync status.
type ContactMessagePage struct {
	Contact  string               `json:"contact"`
	Messages []ContactMessageView `json:"messages"`
	More     bool                 `json:"more"`
	Sync     ContactSyncView      `json:"sync"`
	// Shared is this workspace's agents the contact may message; Directory
	// what the contact last said it shares, a hint for addressing (#222).
	Shared    []string `json:"shared,omitempty"`
	Directory []string `json:"directory,omitempty"`
	// ReviewGrants are what the contact may ask of agents here (#235).
	ReviewGrants []ReviewGrantView `json:"review_grants,omitempty"`
	// Acks are the operator's acknowledgments of this contact's messages
	// (#287), from a broker serving contact_acks.
	Acks *ContactAckSummary `json:"acks,omitempty"`
	// PendingPersonKeys are the person keys the contact signed with that
	// this side's operator hasn't accepted (#115): its operator messages
	// show unverified until one is (contact_op accept_key).
	PendingPersonKeys []string `json:"pending_person_keys,omitempty"`
	// RequireSeals: this side refuses the contact's unsigned messages.
	RequireSeals bool `json:"require_seals,omitempty"`
	// From a broker serving contact_share_sync (#305), read with Shared in
	// one snapshot: SharedSync is whether the contact has answered exactly
	// that set, "confirmed" or "pending"; SharedDigest names the set,
	// SharedAckDigest the one the contact last answered ("" when never),
	// and SharedAckAt when.
	SharedSync      string     `json:"shared_sync,omitempty"`
	SharedDigest    string     `json:"shared_digest,omitempty"`
	SharedAckDigest string     `json:"shared_ack_digest,omitempty"`
	SharedAckAt     *time.Time `json:"shared_ack_at,omitempty"`
}

// DirectoryDigest names a list of agents as a set (#305): sorted, each
// once, each length-prefixed, an unambiguous framing; hex SHA-256.
func DirectoryDigest(agents []string) string {
	set := slices.Clone(agents)
	slices.Sort(set)
	h := sha256.New()
	for _, agent := range slices.Compact(set) {
		h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(agent))))
		h.Write([]byte(agent))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CapabilityContactShareSync: contact pages say whether the contact has
// what this side shares (#305).
const CapabilityContactShareSync = "contact_share_sync"

// CapabilityReviewAskRefs: operator_review asked also reads given requests
// by contact and ask ID (#305).
const CapabilityReviewAskRefs = "review_ask_refs"

// CapabilityAskFindings: asked requests carry the findings their verdict
// sent (#303).
const CapabilityAskFindings = "ask_findings"

// CapabilityReviewAdvisories: a review request queued to a paired
// workspace's agent comes back with what suggests it may be refused (#304).
const CapabilityReviewAdvisories = "review_advisories"

// CapabilityPairedMentions: a ⇄ send may tag this side's agents and
// operator (#278).
const CapabilityPairedMentions = "paired_mentions"

// CapabilityPairingHistory: a contact's history carries every pairing with
// it, each message tagged, and an earlier pairing's messages read or
// acknowledged by contact_epoch (#276). Advertised only to a session that
// opened at PairingHistoryVersion.
const CapabilityPairingHistory = "pairing_history"

// ReviewAskRef names one of this side's requests to a paired workspace.
type ReviewAskRef struct {
	Contact string `json:"contact"`
	AskID   string `json:"ask_id"`
	// PairingEpoch is the pairing it was asked under (#276), 0 for the
	// contact's current one; another needs PairingHistoryVersion.
	PairingEpoch int64 `json:"pairing_epoch,omitempty"`
}

// ReviewAskRefsMax bounds the refs one read names.
const ReviewAskRefsMax = 8

// ValidateReviewAskRefs checks the refs a read names, as supplied: 1 to
// ReviewAskRefsMax, each a contact key and an ask ID.
func ValidateReviewAskRefs(refs []ReviewAskRef) error {
	if len(refs) == 0 || len(refs) > ReviewAskRefsMax {
		return fmt.Errorf("review_ask_refs names 1 to %d requests", ReviewAskRefsMax)
	}
	for _, r := range refs {
		if err := validContactKey(r.Contact); err != nil {
			return err
		}
		if !validAskRef(r.AskID) {
			return errors.New("review_ask_refs needs each ask_id, at most 128 bytes")
		}
		if r.PairingEpoch < 0 {
			return errors.New("review_ask_refs pairing_epoch is 1 or more, or 0 for the current pairing")
		}
	}
	return nil
}

// validAskRef: an ask ID of 1 to 128 bytes, no control characters.
func validAskRef(id string) bool {
	return id != "" && len(id) <= 128 && !strings.ContainsFunc(id, unicode.IsControl)
}

// Shared sync states.
const (
	SharedSyncConfirmed = "confirmed"
	SharedSyncPending   = "pending"
)

// ContactSyncView is what this side knows of syncing with a contact.
// Requested means a session was asked for, not that one ran.
type ContactSyncView struct {
	Contact     string     `json:"contact"`
	Dials       bool       `json:"dials"`          // this side opens the sessions; otherwise it waits to be dialed
	Live        bool       `json:"live,omitempty"` // a live link is open: messages leave at once, either way
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
	ContactOpSend:    {key: true, text: true, id: true, to: true},
	ContactOpList:    {key: true, before: true},
	ContactOpRead:    {key: true, read: true, id: true, epoch: true},
	ContactOpSync:    {key: true},
	ContactOpShare:   {key: true, id: true, agents: true},
	ContactOpUnshare: {key: true, id: true, agents: true},
	// Review requests (#235).
	ContactOpReviewGrant:  {key: true, id: true, agents: true, grant: true},
	ContactOpReviewAsk:    {key: true, id: true, to: true, note: true, ask: true},
	ContactOpReviewDecide: {key: true, id: true, read: true, approve: true},
	ContactOpName:         {key: true, id: true, note: true},
	ContactOpAddress:      {key: true, id: true, text: true},
	ContactOpPause:        {key: true, id: true},
	ContactOpResume:       {key: true, id: true},
	ContactOpAck:          {key: true, id: true, ack: true, epoch: true},
	// Unpairing a contact (#276).
	ContactOpUnpair: {key: true, id: true, epoch: true, confirm: true},
	// Accepting a contact's person key (#115): contact_text is its key ID.
	ContactOpAcceptKey:    {key: true, id: true, text: true},
	ContactOpRequireSeals: {key: true, id: true, text: true},
	// Pending consent (#293): the list spans every contact without a key.
	ContactOpConsentList:   {anyKey: true, consentBefore: true},
	ContactOpConsentDecide: {key: true, id: true, decide: true},
	ContactOpConsentOpt:    {key: true, id: true, consentOn: true},
	ContactOpConsentReset:  {key: true, id: true, scope: true},
	// Known-peer mobility (#284): neither names a contact.
	ContactOpDiscovery:  {noKey: true, id: true, report: true},
	ContactOpCandidates: {noKey: true, id: true, candidates: true},
	ContactOpFind:       {noKey: true, id: true},
}

type contactFieldSet struct {
	key, text, note, read, before, id, to, agents, grant, ask, approve, ack bool
	anyKey, consentBefore, decide, consentOn, scope, epoch, confirm         bool
	noKey, candidates, report                                               bool
}

// ValidateContactRequest checks an operator_contact frame before anything
// is looked up, audited or changed: a known operation with exactly its
// fields, a contact key, a bounded UTF-8 text, and read IDs.
func ValidateContactRequest(msg Message) error {
	allowed, ok := contactFields[msg.ContactOp]
	if !ok {
		return errors.New("contact_op must be send, list, read, sync, share, unshare, review_grant, review_ask, review_decide, name, address, pause, resume, ack, unpair, accept_key, require_seals, " +
			"consent_list, consent_decide, consent_opt, consent_reset, discovery, candidates or find")
	}
	if err := contactFieldsPresent(msg, allowed); err != nil {
		return err
	}
	return errors.Join(validKeyFor(msg, allowed), validContactID(msg, allowed), validContactText(msg, allowed),
		validContactRead(msg, allowed), validContactBefore(msg), validContactTo(msg.ContactTo), validContactAgents(msg, allowed),
		validContactReview(msg, allowed), validContactAddress(msg), validContactAck(msg, allowed), validConsent(msg, allowed),
		validContactEpoch(msg), validContactUnpair(msg, allowed), validContactCandidates(msg, allowed), validContactReport(msg, allowed))
}

// validContactEpoch refuses a pairing epoch below 0; 0 is the current
// pairing (#276).
func validContactEpoch(msg Message) error {
	if msg.ContactEpoch < 0 {
		return errors.New("contact_epoch names a pairing: 1 or more, or 0 for the current one")
	}
	return nil
}

// validContactAddress requires an address change's HOST:PORT: a host and
// a port from 1 to 65535, as a redemption's destination.
func validContactAddress(msg Message) error {
	if msg.ContactOp != ContactOpAddress || peer.CheckDestination(msg.ContactText) == nil {
		return nil
	}
	return errors.New("contact_text must be HOST:PORT, with a port from 1 to 65535")
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
		{"contact_read", msg.ContactRead != nil, allowed.read},
		{"contact_before", msg.ContactBefore != 0, allowed.before},
		{"contact_to", msg.ContactTo != nil, allowed.to},
		{"contact_agents", msg.ContactAgents != nil, allowed.agents},
		{"contact_text", msg.ContactText != "", allowed.text || allowed.note},
		{"contact_review_grant", msg.ContactReviewGrant != nil, allowed.grant},
		{"review_ask", msg.ReviewAsk != nil, allowed.ask},
		{"contact_approve", msg.ContactApprove != nil, allowed.approve},
		{"contact_message", msg.ContactMessage != nil, false},
		{"contact_page", msg.ContactPage != nil, false},
		{"contact_sync", msg.ContactSync != nil, false},
		{"contact_marked", msg.ContactMarked != nil, false},
		{"contact_ack", msg.ContactAck != nil, allowed.ack},
		{"contact_ack_upto", msg.ContactAckUpto != 0, allowed.ack},
		{"contact_epoch", msg.ContactEpoch != 0, allowed.epoch},
		{"contact_confirm", msg.ContactConfirm != "", allowed.confirm},
		{"contact_unpaired", msg.ContactUnpaired != nil, false},
		{"contact_acked", msg.ContactAcked != nil, false},
		{"contact_acks", msg.ContactAcks != nil, false},
		{"contact_consent_before", msg.ContactConsentBefore != 0, allowed.consentBefore},
		{"consent_decide", msg.ConsentDecide != nil, allowed.decide},
		{"consent_on", msg.ConsentOn != nil, allowed.consentOn},
		{"consent_agent", msg.ConsentAgent != "", allowed.scope},
		{"consent_operation", msg.ConsentOperation != "", allowed.scope},
		{"consent_page", msg.ConsentPage != nil, false},
		{"consent_result", msg.ConsentResult != nil, false},
		{"contact_candidates", msg.ContactCandidates != nil, allowed.candidates},
		{"contact_discovery", msg.ContactDiscovery != nil, false},
		{"contact_candidates_result", msg.ContactCandidatesResult != nil, false},
		{"contact_found", msg.ContactFound != nil, allowed.report},
		{"contact_devices", msg.ContactDevices != nil, allowed.report},
		{"contact_announcing", msg.ContactAnnouncing != nil, allowed.report},
		{"contact_find_generation", msg.ContactFindGeneration != 0, allowed.report},
		{"contact_find", msg.ContactFind != nil, false},
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
	if !textChecked(msg, allowed) {
		return nil
	}
	if !validText(msg.ContactText) {
		return fmt.Errorf("contact_text is required: valid UTF-8, at most %d bytes", ContactTextBytes)
	}
	return nil
}

// validText: 1 to ContactTextBytes of valid UTF-8.
func validText(text string) bool {
	if text == "" || len(text) > ContactTextBytes {
		return false
	}
	return utf8.ValidString(text)
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

// validContactTo: the contact's agents a send is to, none for its operator;
// distinct, each a valid agent ID.
func validContactTo(to []string) error {
	if len(to) > ContactAgentsMax || !distinctAgents(to) {
		return fmt.Errorf("contact_to names up to %d distinct agents", ContactAgentsMax)
	}
	return nil
}

// validContactAgents: the agents share and unshare name, "*" alone for all.
func validContactAgents(msg Message, allowed contactFieldSet) error {
	agents := msg.ContactAgents
	if !allowed.agents || everyAgent(agents) {
		return nil
	}
	if len(agents) == 0 || validContactTo(agents) != nil {
		return fmt.Errorf("contact_agents is \"*\" or 1 to %d distinct agents", ContactAgentsMax)
	}
	return nil
}

// everyAgent: "*" alone.
func everyAgent(agents []string) bool { return len(agents) == 1 && agents[0] == Broadcast }

// distinctAgents: each a valid agent ID, none twice.
func distinctAgents(agents []string) bool {
	seen := map[string]bool{}
	for _, a := range agents {
		if ValidateAgentID(a) != nil || seen[a] {
			return false
		}
		seen[a] = true
	}
	return true
}

// textChecked: the operation takes text, or a note it was given.
func textChecked(msg Message, allowed contactFieldSet) bool {
	if allowed.text {
		return true
	}
	return allowed.note && msg.ContactText != ""
}

// ContactReviewGrant is what a contact may ask of agents here (#235).
type ContactReviewGrant struct {
	Mode    string `json:"mode"` // ask, auto or off
	Publish bool   `json:"publish,omitempty"`
	Formal  bool   `json:"formal,omitempty"` // needs publish
}

// ReviewGrantView is one agent's review grant for a contact.
type ReviewGrantView struct {
	Agent   string `json:"agent"`
	Mode    string `json:"mode"`
	Publish bool   `json:"publish,omitempty"`
	Formal  bool   `json:"formal,omitempty"`
}

// reviewModes are the modes a review grant takes.
var reviewModes = map[string]bool{"ask": true, "auto": true, "off": true}

// validContactReview checks the review ops' own fields: a grant, a
// request for one agent pinned to a change, or one decision.
func validContactReview(msg Message, allowed contactFieldSet) error {
	switch {
	case allowed.grant:
		return validReviewGrant(msg.ContactReviewGrant)
	case allowed.ask:
		return validReviewAsk(msg)
	case allowed.approve:
		return validReviewDecide(msg)
	}
	return nil
}

func validReviewGrant(g *ContactReviewGrant) error {
	if g == nil || !reviewModes[g.Mode] {
		return errors.New("contact_review_grant needs a mode: ask, auto or off")
	}
	if g.Formal && !g.Publish {
		return errors.New("contact_review_grant: formal needs publish")
	}
	return nil
}

func validReviewAsk(msg Message) error {
	a := msg.ReviewAsk
	if len(msg.ContactTo) != 1 || a == nil {
		return errors.New("review_ask asks exactly one agent in contact_to")
	}
	if slices.Contains([]string{a.Forge, a.Repository, a.Change}, "") {
		return errors.New("review_ask needs the change: forge, repository and change")
	}
	if a.Base == "" || a.Head == "" {
		return errors.New("review_ask needs both pins: base and head")
	}
	return nil
}

func validReviewDecide(msg Message) error {
	if len(msg.ContactRead) != 1 || msg.ContactApprove == nil {
		return errors.New("review_decide takes one request ID in contact_read and contact_approve")
	}
	return nil
}

// Acknowledgment states of a received message (#287).
const (
	AckBefore  = "before"    // received before acknowledgments were tracked
	AckNeeded  = "needs_ack" // waiting for the operator
	AckDone    = "acked"     // the operator acknowledged it
	AckExpired = "expired"   // dropped unacknowledged past UnackedKept, never acknowledged
)

// UnackedKept bounds the messages one contact keeps waiting for the
// operator's acknowledgment; past it the oldest expire, counted.
const UnackedKept = 1024

// ContactAckSummary is where a contact's acknowledgments stand: how many
// wait, the newest cursor an "everything" acknowledgment may cover, read
// with that count, and how many expired unacknowledged, ever.
type ContactAckSummary struct {
	Unacked    int   `json:"unacked"`
	AckUpto    int64 `json:"ack_upto"`
	AckExpired int   `json:"ack_expired"`
}

// validContactAck requires exactly one selector: 1 to ContactReadMax IDs,
// or a positive cursor.
func validContactAck(msg Message, allowed contactFieldSet) error {
	if !allowed.ack {
		return nil
	}
	ids, upto := msg.ContactAck != nil, msg.ContactAckUpto != 0
	switch {
	case ids == upto:
		return errors.New("contact_ack or contact_ack_upto, exactly one")
	case upto && msg.ContactAckUpto < 0:
		return errors.New("contact_ack_upto is a positive cursor")
	case upto:
		return nil
	}
	return validAckIDs(msg.ContactAck)
}

func validAckIDs(ids []string) error {
	if len(ids) == 0 || len(ids) > ContactReadMax {
		return fmt.Errorf("contact_ack takes 1 to %d message IDs", ContactReadMax)
	}
	for _, id := range ids {
		if !validMessageIDText(id) {
			return fmt.Errorf("contact_ack IDs are 1 to %d printable bytes", ContactIDBytes)
		}
	}
	return nil
}
