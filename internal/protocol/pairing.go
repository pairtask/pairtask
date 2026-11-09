package protocol

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Operator pairing (#178): the operator opens a short-code invitation and
// decides the resulting pairing requests. Operator socket only; agents have
// no pairing frames. The invitation code appears in exactly one place: the
// direct response to op=open. It is never stored, emitted, audited or echoed
// by status or list.
const (
	TypeOperatorPairing       = "operator_pairing"        // operator → broker
	TypeOperatorPairingResult = "operator_pairing_result" // broker → operator

	PairingOpOpen   = "open"
	PairingOpCancel = "cancel" // target: the full invitation ID
	PairingOpStatus = "status"
	PairingOpList   = "list"
	PairingOpAccept = "accept" // target: the full identity key
	PairingOpReject = "reject" // target: the full identity key
	// PairingOpRedeem redeems another workspace's code: pairing_destination,
	// pairing_code and an optional pairing_name; it answers with pairing_join
	// as soon as the redemption is recorded, before any network wait.
	PairingOpRedeem = "redeem"
	// PairingOpSettle queues settlement of one join (pairing_target, its
	// operation ID) or of every candidate, and answers with what it queued.
	PairingOpSettle = "settle"

	// ErrPairing is a refused pairing operation; the detail names why.
	ErrPairing = "pairing_refused"
	// ErrPairingPeerTooOld: the other side spoke only the legacy pairing
	// exchange, which can't pair an identity this side retains or a
	// declared repair (#276); it must update to pair again.
	ErrPairingPeerTooOld = "pairing_peer_too_old"
	// ErrIdentityStorage: this side's store holds as many identities as it
	// retains, so it can't take another (#276).
	ErrIdentityStorage = "identity_storage"

	// CapabilityPairing: the broker serves operator_pairing.
	CapabilityPairing = "pairing"
	// CapabilityPairingRedeem: the broker serves operator_pairing redeem.
	CapabilityPairingRedeem = "pairing_redeem"
	// CapabilityPairingContacts: the broker settles joins and reports contacts.
	CapabilityPairingContacts = "pairing_contacts"
	// CapabilityPairingDiscoverable: open takes pairing_discoverable (#284).
	CapabilityPairingDiscoverable = "pairing_discoverable"
	// CapabilityPairingContactSummary: each listed contact carries its sync
	// state and the review requests waiting for this side.
	CapabilityPairingContactSummary = "pairing_contact_summary"
	// EventPairingChanged tells the operator session that durable pairing
	// state changed in the background; it carries nothing, so a client
	// reads the pairing state again.
	EventPairingChanged = "pairing_changed"

	// ListedPairingRequests bounds a pairing listing: pending first, then
	// by identity key. Totals and the truncation flag keep omissions visible.
	ListedPairingRequests = 64
	// ListedPairingJoins bounds the joins in a pairing snapshot, newest first.
	ListedPairingJoins = 16
	// ListedPairingContacts bounds the contacts in a pairing snapshot.
	ListedPairingContacts = 64

	// PairingPreview says what pairing can and can't do yet, wherever a code
	// is shown.
	PairingPreview = "experimental, unaudited CPace: a code is redeemable only through a broker's pairing socket, and contact messages (pairtask operator peer) are experimental too"
)

// PairingSnapshot is the open invitation, if any, and the pairing requests.
type PairingSnapshot struct {
	// SelfIdentity is this workspace's pairtask SPKI fingerprint, for the
	// operators to compare by phone; IdentityError says why it is unavailable.
	SelfIdentity string `json:"self_identity,omitempty"`
	SelfName     string `json:"self_name,omitempty"` // the name this workspace pairs under
	// NamesAvailable: the broker serves contact op name; an older one
	// leaves it false, so a client refuses before sending one.
	NamesAvailable bool `json:"names_available,omitempty"`
	// PauseAvailable: the broker serves contact ops pause and resume (#276).
	PauseAvailable bool `json:"pause_available,omitempty"`
	// UnpairAvailable: the broker serves contact op unpair; RepairAvailable,
	// pairing_repair on open and redeem (#276).
	UnpairAvailable bool `json:"unpair_available,omitempty"`
	RepairAvailable bool `json:"repair_available,omitempty"`
	// AcksAvailable: the broker serves contact op ack (#287).
	AcksAvailable     bool                 `json:"acks_available,omitempty"`
	IdentityError     string               `json:"identity_error,omitempty"`
	Invitation        *InvitationView      `json:"invitation,omitempty"`
	Requests          []PairingRequestView `json:"requests"`
	RequestsTotal     int                  `json:"requests_total"`
	RequestsTruncated bool                 `json:"requests_truncated"`
	// Joins are this workspace's redemptions of other workspaces' codes.
	Joins           []PairingJoinView `json:"joins"`
	JoinsTotal      int               `json:"joins_total"`
	JoinsTruncated  bool              `json:"joins_truncated"`
	RedeemAvailable bool              `json:"redeem_available"`
	// Contacts are the workspaces paired in either direction, newest first.
	Contacts          []PairingContactView `json:"contacts"`
	ContactsTotal     int                  `json:"contacts_total"`
	ContactsTruncated bool                 `json:"contacts_truncated"`
}

// PairingContactView is one paired workspace. Confirmed means both sides
// durably hold the decision: this side's delivery as inviter, or a confirmed
// join as joiner; until then it is still confirming.
type PairingContactView struct {
	IdentityKey   string    `json:"identity_key"`
	Name          string    `json:"name"`
	Destination   string    `json:"destination"`
	EstablishedAt time.Time `json:"established_at"`
	Joined        bool      `json:"joined"`
	Invited       bool      `json:"invited"`
	Confirmed     bool      `json:"confirmed"`
	// LocalName is what this side's operator calls it; Dials, that this
	// side opens the sessions with it.
	LocalName string `json:"local_name,omitempty"`
	Dials     bool   `json:"dials,omitempty"`
	// PausedAt is when this side's operator paused it (#276): no traffic
	// either way until resumed; nil while active.
	PausedAt *time.Time `json:"paused_at,omitempty"`
	// UnpairedAt is when this side's operator unpaired it (#276): its
	// history stays readable, nothing more travels; nil while paired.
	// PairingEpoch is its current or last pairing; Legacy, that the
	// pairing was made by the legacy exchange, without a pairing ID.
	UnpairedAt   *time.Time `json:"unpaired_at,omitempty"`
	PairingEpoch int64      `json:"pairing_epoch"`
	Legacy       bool       `json:"legacy,omitempty"`
	// Summary is nil from a broker that reports none: unknown, not idle.
	Summary *PairingContactSummary `json:"summary,omitempty"`
	// Acks are the operator's acknowledgments of its messages (#287), from
	// a broker serving contact_acks.
	Acks *ContactAckSummary `json:"acks,omitempty"`
	// PeerState is what the other side last told this one of itself
	// (#276): paused or unpaired, empty while active or unknown, with
	// PeerStateAt when it was stored. PeerStateAvailable is unavailable
	// only once the peer is known to speak a version without notices.
	PeerState          string     `json:"peer_state,omitempty"`
	PeerStateAt        *time.Time `json:"peer_state_at,omitempty"`
	PeerStateAvailable string     `json:"peer_state_available,omitempty"`
	// Notice is this side's newest notice of the current pairing while the
	// peer hasn't answered it: pending, or undelivered after its last
	// attempt; NoticeAttempts counts its attempts. Neither claims the peer
	// knows.
	Notice         string `json:"notice,omitempty"`
	NoticeAttempts int    `json:"notice_attempts,omitempty"`
}

// PairingContactSummary is how a contact stands now: its sync as this broker
// observed it since it started, and its review requests waiting for this
// side's operator.
type PairingContactSummary struct {
	Sync            ContactSyncView `json:"sync"`
	ReviewsAwaiting int             `json:"reviews_awaiting"`
}

// PairingSettleView is what a settle request scheduled locally; nothing in
// it says the peer was reached.
type PairingSettleView struct {
	Queued        int `json:"queued"`
	AlreadyQueued int `json:"already_queued"`
	NotQueued     int `json:"not_queued"`
}

// PairingJoinView is one redemption as stored. awaiting_acceptance means the
// inviter committed it and has yet to decide; it is not a contact.
type PairingJoinView struct {
	OperationID  string    `json:"operation_id"`
	State        string    `json:"state"`
	Destination  string    `json:"destination"`
	Name         string    `json:"name"`
	Peer         string    `json:"peer"`
	InvitationID string    `json:"invitation_id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	// DecisionConfirmedAt is set once the inviter confirmed our receipt of
	// its decision; SettleError is the last settlement's fixed cause.
	DecisionConfirmedAt *time.Time `json:"decision_confirmed_at,omitempty"`
	SettleError         string     `json:"settle_error,omitempty"`
	// Kind and RepairsUnpairedAt are as on a request (#276).
	Kind              string     `json:"kind,omitempty"`
	RepairsUnpairedAt *time.Time `json:"repairs_unpaired_at,omitempty"`
}

// InvitationView describes the open invitation. Code is set only in the
// response to op=open.
type InvitationView struct {
	ID           string    `json:"id"`
	ExpiresAt    time.Time `json:"expires_at"`
	AttemptsLeft int       `json:"attempts_left"`
	InFlight     bool      `json:"in_flight"`
	Consumed     bool      `json:"consumed"`
	Expired      bool      `json:"expired"`
	Code         string    `json:"code,omitempty"`
	// Discoverable: announced by PublicName on the local network while open
	// (#284); AnnounceState is what the helper reported (Announce* values).
	Discoverable  bool   `json:"discoverable,omitempty"`
	PublicName    string `json:"public_name,omitempty"`
	AnnounceState string `json:"announce_state,omitempty"`
}

// PairingRequestView is one pairing request. Until accepted it is not a
// contact; names are display metadata only.
type PairingRequestView struct {
	IdentityKey   string    `json:"identity_key"`
	Name          string    `json:"name"`
	State         string    `json:"state"`
	FirstPairedAt time.Time `json:"first_paired_at"`
	// DeliveredAt is when the joiner acknowledged the decision.
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	// Kind is what the exchange is (#276): pair, repair or address_move,
	// display only; RepairsUnpairedAt is when this side unpaired the
	// identity it pairs with again, nil when it isn't unpaired.
	Kind              string     `json:"kind,omitempty"`
	RepairsUnpairedAt *time.Time `json:"repairs_unpaired_at,omitempty"`
}

var (
	ErrPairingNoInvitation = errors.New("no open invitation")
	ErrPairingEmptyKey     = errors.New("type the start of the key after sha256:")
	ErrPairingAmbiguous    = errors.New("matches more than one request; type more of the key")
	ErrPairingPartial      = errors.New("the request list is partial; use a full listed key")
	ErrPairingUnknown      = errors.New("no listed request starts with it")
)

// ResolveKey resolves a typed prefix to the one listed identity key it
// starts, so a decision always names a full key. Keys are compared without
// their "sha256:" scheme, which every key shares, so only digest characters
// choose a request. An empty prefix never resolves, an exact key always does,
// and a partial listing can't prove a prefix unique.
func (s *PairingSnapshot) ResolveKey(prefix string) (string, error) {
	typed := KeyDigest(strings.TrimSpace(prefix))
	if !digestTyped(typed) {
		return "", ErrPairingEmptyKey
	}
	exact, matches := s.keysStarting(typed)
	if exact != "" {
		return exact, nil
	}
	switch {
	case len(matches) > 1:
		return "", ErrPairingAmbiguous
	case s.RequestsTruncated:
		return "", ErrPairingPartial
	case len(matches) == 0:
		return "", ErrPairingUnknown
	}
	return matches[0], nil
}

// digestTyped: something of a digest was typed, not only the scheme.
func digestTyped(typed string) bool {
	return typed != "" && typed != "sha256"
}

// keysStarting is the listed key whose digest is typed, or else every
// listed key whose digest starts with it.
func (s *PairingSnapshot) keysStarting(typed string) (string, []string) {
	var matches []string
	for _, r := range s.Requests {
		digest := KeyDigest(r.IdentityKey)
		if digest == typed {
			return r.IdentityKey, nil
		}
		if strings.HasPrefix(digest, typed) {
			matches = append(matches, r.IdentityKey)
		}
	}
	return "", matches
}

// KeyDigest is an identity key without its "sha256:" scheme: the part that
// tells keys apart.
func KeyDigest(key string) string {
	digest, _ := strings.CutPrefix(key, "sha256:")
	return digest
}

// ShortKey is the first 12 digest characters of an identity key, enough to
// tell contacts apart on screen; the full key stays authoritative.
func ShortKey(key string) string {
	digest := KeyDigest(key)
	return digest[:min(len(digest), 12)]
}

// InvitationID is the open invitation's ID, for a cancel.
func (s *PairingSnapshot) InvitationID() (string, error) {
	if s.Invitation == nil {
		return "", ErrPairingNoInvitation
	}
	return s.Invitation.ID, nil
}

// Summary describes the invitation's state, never its code.
func (v InvitationView) Summary() string {
	switch {
	case v.Consumed:
		return "used"
	case v.Expired:
		return "expired"
	case v.InFlight:
		return "a pairing attempt is in progress"
	}
	return fmt.Sprintf("open until %s, %d attempts left", v.ExpiresAt.Local().Format("15:04"), v.AttemptsLeft)
}

var pairingStateLabels = map[string]string{
	"pending_acceptance": "waiting for your decision",
	"accepted":           "accepted",
	"rejected":           "rejected",
}

// Label names the request's state for people: an acceptance the joiner has
// not yet acknowledged is not a contact on both sides.
func (v PairingRequestView) Label() string {
	if v.State == "accepted" && v.DeliveredAt == nil {
		return "accepted, awaiting peer confirmation"
	}
	return PairingStateLabel(v.State)
}

// PairingStateLabel names a request state for people; an unknown one is
// quoted, so it can't carry terminal controls.
func PairingStateLabel(state string) string {
	if label, ok := pairingStateLabels[state]; ok {
		return label
	}
	return strconv.Quote(state)
}
