package pairbook

import (
	"crypto/ed25519"
	"slices"
	"strings"
	"time"
)

// relayReachDomain separates relay reachability records from every other
// kind; Address records and their checks are untouched by it.
const relayReachDomain = "pairbook-relay-reach-v1"

// Relay reachability limits.
const (
	RelayReachVersion  = 1               // the only version this client writes and reads
	MaxRelays          = 4               // relays in one record, tried in order
	RelayReachValidity = AddressValidity // a record's ExpiresAt - IssuedAt, at most
)

// RelayReach is where a node that hosts no address can be reached: the IDs
// of the relays it keeps a listen slot on, in the order a dialer tries
// them. A dialer resolves each ID to an origin from its own Pairbook,
// never from this record. It is a record of its own, beside Address, so a
// dialer that doesn't know it never sees it.
type RelayReach struct {
	Version   int
	Space     string
	Node      ed25519.PublicKey
	Relays    []string
	IssuedAt  int64
	ExpiresAt int64
}

// SignedRelayReach is a relay reachability record signed by its own node key.
type SignedRelayReach struct {
	RelayReach
	Sig []byte
}

// SigningBytes are the bytes a relay reachability record's signature covers.
func (r RelayReach) SigningBytes() []byte {
	return newEncoder(relayReachDomain).time(int64(r.Version)).str(r.Space).bytes(r.Node).
		strs(r.Relays).time(r.IssuedAt).time(r.ExpiresAt).b
}

// SignRelayReach signs r with the node's key.
func SignRelayReach(node ed25519.PrivateKey, r RelayReach) SignedRelayReach {
	return SignedRelayReach{RelayReach: r, Sig: sign(node, r.SigningBytes())}
}

// Verify checks the record is of a known version, belongs to space, is
// signed by its Node, lists 1 to MaxRelays distinct relay IDs, and is valid
// at now. Whether Node is a member is the caller's check.
func (s SignedRelayReach) Verify(space string, now time.Time) error {
	if s.Version != RelayReachVersion || s.Space != space {
		return ErrInvalid
	}
	if !s.wellFormed() {
		return ErrInvalid
	}
	return period{s.IssuedAt, s.ExpiresAt}.check(RelayReachValidity, now)
}

func (s SignedRelayReach) wellFormed() bool {
	return relayIDs(s.Relays).valid() && signedBy(s.Node, s.SigningBytes(), s.Sig)
}

type relayIDs []string

// valid: 1 to MaxRelays entries, each a relay ID, none repeated.
func (l relayIDs) valid() bool {
	if len(l) == 0 || len(l) > MaxRelays {
		return false
	}
	if slices.ContainsFunc(l, func(id string) bool { return !ValidRelayID(id) }) {
		return false
	}
	return len(slices.Compact(slices.Sorted(slices.Values(l)))) == len(l)
}

// ValidRelayID: 16 bytes as 32 lowercase hex characters.
func ValidRelayID(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
}
