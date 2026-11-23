package pairbook

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
)

func relayReachFixture(t *testing.T) (ed25519.PrivateKey, RelayReach, time.Time) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1_800_000_000_000)
	r := RelayReach{
		Version: RelayReachVersion, Space: "space-a", Node: key.Public().(ed25519.PublicKey),
		Relays:   []string{strings.Repeat("ab", 16), strings.Repeat("0f", 16)},
		IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(),
	}
	return key, r, now
}

func TestRelayReachVerifies(t *testing.T) {
	key, r, now := relayReachFixture(t)
	if err := SignRelayReach(key, r).Verify("space-a", now); err != nil {
		t.Fatalf("a well-formed record: %v", err)
	}
}

func TestRelayReachRefusals(t *testing.T) {
	key, base, now := relayReachFixture(t)
	_, other, _ := ed25519.GenerateKey(nil)
	for _, tc := range []struct {
		name   string
		change func(*RelayReach)
		space  string
		at     time.Time
		signer ed25519.PrivateKey
		want   error
	}{
		{"unknown version", func(r *RelayReach) { r.Version = 2 }, "space-a", now, key, ErrInvalid},
		{"other space", func(*RelayReach) {}, "space-b", now, key, ErrInvalid},
		{"no relays", func(r *RelayReach) { r.Relays = nil }, "space-a", now, key, ErrInvalid},
		{"too many relays", func(r *RelayReach) {
			r.Relays = []string{strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32), strings.Repeat("e", 32)}
		}, "space-a", now, key, ErrInvalid},
		{"repeated relay", func(r *RelayReach) { r.Relays = []string{r.Relays[0], r.Relays[0]} }, "space-a", now, key, ErrInvalid},
		{"uppercase relay ID", func(r *RelayReach) { r.Relays = []string{strings.Repeat("AB", 16)} }, "space-a", now, key, ErrInvalid},
		{"short relay ID", func(r *RelayReach) { r.Relays = []string{"abcd"} }, "space-a", now, key, ErrInvalid},
		{"a URL instead of an ID", func(r *RelayReach) { r.Relays = []string{"https://relay.example"} }, "space-a", now, key, ErrInvalid},
		{"signed by another key", func(*RelayReach) {}, "space-a", now, other, ErrInvalid},
		{"longer than a day", func(r *RelayReach) { r.ExpiresAt = now.Add(25 * time.Hour).UnixMilli() }, "space-a", now, key, ErrInvalid},
		{"expired", func(*RelayReach) {}, "space-a", now.Add(2 * time.Hour), key, ErrExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			r.Relays = append([]string(nil), base.Relays...)
			tc.change(&r)
			if err := SignRelayReach(tc.signer, r).Verify(tc.space, tc.at); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// The signature covers the relay order: a dialer tries relays in the order
// the node chose, and no one else can reorder them.
func TestRelayReachOrderIsSigned(t *testing.T) {
	key, r, now := relayReachFixture(t)
	signed := SignRelayReach(key, r)
	signed.Relays = []string{r.Relays[1], r.Relays[0]}
	if err := signed.Verify("space-a", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a reordered record verified: %v", err)
	}
}

// A relay reachability signature never verifies as an address record.
func TestRelayReachDomainSeparation(t *testing.T) {
	key, r, now := relayReachFixture(t)
	sig := SignRelayReach(key, r).Sig
	a := SignedAddress{Address: Address{Space: r.Space, Node: r.Node, Addrs: []string{"relay.example:443"}, IssuedAt: r.IssuedAt, ExpiresAt: r.ExpiresAt}, Sig: sig}
	if err := a.Verify("space-a", now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a relay reachability signature verified as an address: %v", err)
	}
}
