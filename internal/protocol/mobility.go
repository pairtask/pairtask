package protocol

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pairtask/pairtask/internal/peer"
)

// Known-peer mobility (#284): a host helper finds where a paired workspace
// went after its address changed, and hands the broker endpoints to try.
// The broker alone decides: it probes each one, and moves a contact there
// only when the workspace there presents that contact's pinned identity.
// Neither operation names a contact; both are one-shot, never a session.

const (
	// ContactOpDiscovery is the helper's lease, under message_id: whether
	// to advertise this workspace's presence and whether to browse for
	// others, the ports failed contacts were last reached on, and until
	// when. It takes no contact.
	ContactOpDiscovery = "discovery"
	// CapabilityContactDiscovery: the broker serves discovery.
	CapabilityContactDiscovery = "contact_discovery"
	// ContactOpCandidates hands the broker endpoints, in contact_candidates,
	// under message_id. The one answer comes after the probes: an outcome
	// per endpoint and the pairing snapshot.
	ContactOpCandidates = "candidates"
	// CapabilityContactCandidates: the broker serves candidates.
	CapabilityContactCandidates = "contact_candidates"

	// MaxCandidates bounds one candidates operation, and DiscoveryPorts a
	// lease's port hints.
	MaxCandidates  = 8
	DiscoveryPorts = 8
	// DiscoveryLease is how long a lease holds at most; DiscoveryShortLease
	// while an invitation is announced or the operator is finding, so the
	// helper comes back within seconds and a cancel is off the air quickly.
	DiscoveryLease      = 90 * time.Second
	DiscoveryShortLease = 20 * time.Second
	// DiscoveryHelperStale: a helper that asked for no lease this long is
	// off, or not installed, for every decision that needs it.
	DiscoveryHelperStale = 90 * time.Second
)

// Finding colleagues (#284): an invitation the operator made discoverable is
// announced by name on the local network while it is open; /pair find lists
// the named invitations the helper hears and the online tailnet devices.
// Nothing listed is verified: the pairing exchange decides who is there.
const (
	// ContactOpFind reads the helper's latest snapshot, under message_id.
	ContactOpFind = "find"
	// CapabilityContactFind: the broker serves find and takes the helper's
	// report with discovery.
	CapabilityContactFind = "contact_find"
	// MaxFound and MaxDevices bound a report; NameBytesMax an announced
	// name (one TXT string).
	MaxFound     = 16
	MaxDevices   = 32
	NameBytesMax = 63
)

// Announcement states of a discoverable invitation, as the broker knows
// them from the helper's reports; none says a peer heard anything.
const (
	AnnounceRequested  = "requested"   // no report since the invitation opened
	AnnounceAnnouncing = "announcing"  // the helper reported an advert left within DiscoveryHelperStale
	AnnounceFailed     = "failed"      // the helper reported it could not send
	AnnounceHelperLost = "helper_lost" // no lease asked for DiscoveryHelperStale
)

// DiscoveryInvitation is what a lease asks the helper to announce.
type DiscoveryInvitation struct {
	ID        string    `json:"id"` // the invitation's ID, so a replacement with the same name is a new announcement; never announced
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
}

// FoundInvitation is a named invitation the helper heard on the local
// network: an untrusted announcement, not an identity.
type FoundInvitation struct {
	Name   string    `json:"name"`
	Host   string    `json:"host"`
	Port   int       `json:"port"`
	SeenAt time.Time `json:"seen_at"`
}

// TailnetDevice is an online tailnet node: a device, not a listener.
type TailnetDevice struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// Announcing is the helper's word on the invitation it was asked to
// announce: whether an advert datagram left this host, and the failure if not.
type Announcing struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Sent  bool   `json:"sent"`
	Error string `json:"error,omitempty"`
}

// ContactFind is find's answer: when the helper last asked for a lease
// (nil: never since this broker started) and its latest snapshot.
type ContactFind struct {
	HelperSeenAt *time.Time        `json:"helper_seen_at"`
	Invitations  []FoundInvitation `json:"invitations"`
	Devices      []TailnetDevice   `json:"devices"`
}

// Candidate outcomes.
const (
	CandidateAdopted     = "adopted"     // the contact moved there
	CandidateNoMatch     = "no_match"    // the workspace there is none this side is looking for
	CandidateUnreachable = "unreachable" // nothing answered, or no handshake
	CandidateRefused     = "refused"     // not dialled by policy, or the move was fenced
	CandidateSkipped     = "skipped"     // nothing to look for, or the route works again
)

// ContactDiscovery is a lease. Advertise: a contact this side doesn't dial
// may be looking for it. Browse: a contact this side dials is failing.
// Ports are where those failing contacts were last reached, so a name or
// overlay address can be tried on the right port. Invitation: announce
// this name while it is set. Find: the operator is looking for colleagues.
type ContactDiscovery struct {
	Advertise  bool                 `json:"advertise"`
	Browse     bool                 `json:"browse"`
	Ports      []int                `json:"ports"`
	ExpiresAt  time.Time            `json:"expires_at"`
	Invitation *DiscoveryInvitation `json:"invitation,omitempty"`
	Find       bool                 `json:"find,omitempty"`
}

// validContactReport bounds the helper's report on discovery: at most
// MaxFound invitations and MaxDevices devices, each name within
// NameBytesMax bytes of valid UTF-8 without control characters, each
// found host and port a destination the broker could dial.
func validContactReport(msg Message, allowed contactFieldSet) error {
	if !allowed.report {
		return nil
	}
	if len(msg.ContactFound) > MaxFound || len(msg.ContactDevices) > MaxDevices {
		return fmt.Errorf("contact_found takes at most %d invitations and contact_devices at most %d devices", MaxFound, MaxDevices)
	}
	for _, f := range msg.ContactFound {
		if !ValidDisplayName(f.Name) || peer.CheckDestination(net.JoinHostPort(f.Host, strconv.Itoa(f.Port))) != nil {
			return errors.New("contact_found entries have a display name, a host and a port from 1 to 65535")
		}
	}
	for _, d := range msg.ContactDevices {
		if len(d.Name) > 253 || len(d.Address) > 64 || (d.Name == "" && d.Address == "") || !printable(d.Name) || !printable(d.Address) {
			return errors.New("contact_devices entries have a name or an address, each printable and bounded")
		}
	}
	if a := msg.ContactAnnouncing; a != nil && (!ValidDisplayName(a.Name) || len(a.ID) > 128 || len(a.Error) > 256 || !printable(a.Error)) {
		return errors.New("contact_announcing has an invitation ID, a display name and an error of at most 256 bytes")
	}
	return nil
}

// ValidDisplayName: at most NameBytesMax bytes of valid UTF-8, printable
// with no whitespace but the space, not blank; the same rule the host
// helper applies, so a name the broker accepts is one it announces.
func ValidDisplayName(name string) bool {
	if name == "" || len(name) > NameBytesMax || strings.TrimSpace(name) == "" || !utf8.ValidString(name) {
		return false
	}
	return printable(name)
}

// printable: every rune prints, and the only whitespace is the space.
func printable(text string) bool {
	for _, r := range text {
		if !unicode.IsPrint(r) || (unicode.IsSpace(r) && r != ' ') {
			return false
		}
	}
	return true
}

// CandidateResult is what became of one endpoint; Contact is set only
// when the contact moved there.
type CandidateResult struct {
	Candidate string `json:"candidate"`
	Outcome   string `json:"outcome"`
	Contact   string `json:"contact,omitempty"`
}

// validContactCandidates requires 1 to MaxCandidates distinct endpoints,
// each a HOST:PORT a redemption could dial.
func validContactCandidates(msg Message, allowed contactFieldSet) error {
	if !allowed.candidates {
		return nil
	}
	n := len(msg.ContactCandidates)
	if n == 0 || n > MaxCandidates {
		return fmt.Errorf("contact_candidates takes 1 to %d HOST:PORT endpoints", MaxCandidates)
	}
	seen := map[string]bool{}
	for _, c := range msg.ContactCandidates {
		if peer.CheckDestination(c) != nil || seen[c] {
			return errors.New("contact_candidates are distinct HOST:PORT endpoints, each with a port from 1 to 65535")
		}
		seen[c] = true
	}
	return nil
}
