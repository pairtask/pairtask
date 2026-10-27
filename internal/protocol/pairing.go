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

	// ErrPairing is a refused pairing operation; the detail names why.
	ErrPairing = "pairing_refused"

	// CapabilityPairing: the broker serves operator_pairing.
	CapabilityPairing = "pairing"

	// ListedPairingRequests bounds a pairing listing: pending first, then
	// by identity key. Totals and the truncation flag keep omissions visible.
	ListedPairingRequests = 64

	// PairingPreview says what this slice can't do yet, wherever a code is
	// shown, so opening one never implies a peer can redeem it.
	PairingPreview = "preview: nothing redeems this code yet; joining another workspace and contact messaging aren't connected"
)

// PairingSnapshot is the open invitation, if any, and the pairing requests.
type PairingSnapshot struct {
	Invitation        *InvitationView      `json:"invitation,omitempty"`
	Requests          []PairingRequestView `json:"requests"`
	RequestsTotal     int                  `json:"requests_total"`
	RequestsTruncated bool                 `json:"requests_truncated"`
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
}

// PairingRequestView is one pairing request. Until accepted it is not a
// contact; names are display metadata only.
type PairingRequestView struct {
	IdentityKey   string    `json:"identity_key"`
	Name          string    `json:"name"`
	State         string    `json:"state"`
	FirstPairedAt time.Time `json:"first_paired_at"`
}

var (
	ErrPairingNoInvitation = errors.New("no open invitation")
	ErrPairingEmptyKey     = errors.New("a key or key prefix is required")
	ErrPairingAmbiguous    = errors.New("matches more than one request; type more of the key")
	ErrPairingPartial      = errors.New("the request list is partial; use a full listed key")
	ErrPairingUnknown      = errors.New("no listed request starts with it")
)

// ResolveKey resolves a typed prefix to the one listed identity key it
// starts, so a decision always names a full key. An empty prefix never
// resolves, an exact key always does, and a partial listing can't prove a
// prefix unique.
func (s *PairingSnapshot) ResolveKey(prefix string) (string, error) {
	if strings.TrimSpace(prefix) == "" {
		return "", ErrPairingEmptyKey
	}
	var matches []string
	for _, r := range s.Requests {
		if r.IdentityKey == prefix {
			return prefix, nil
		}
		if strings.HasPrefix(r.IdentityKey, prefix) {
			matches = append(matches, r.IdentityKey)
		}
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

// PairingStateLabel names a request state for people; an unknown one is
// quoted, so it can't carry terminal controls.
func PairingStateLabel(state string) string {
	if label, ok := pairingStateLabels[state]; ok {
		return label
	}
	return strconv.Quote(state)
}
