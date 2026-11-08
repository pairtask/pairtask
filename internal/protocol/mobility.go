package protocol

import (
	"errors"
	"fmt"
	"time"

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
	// DiscoveryLease is how long a lease holds at most.
	DiscoveryLease = 90 * time.Second
)

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
// overlay address can be tried on the right port; nothing else is said.
type ContactDiscovery struct {
	Advertise bool      `json:"advertise"`
	Browse    bool      `json:"browse"`
	Ports     []int     `json:"ports"`
	ExpiresAt time.Time `json:"expires_at"`
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
