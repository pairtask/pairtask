package protocol_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairingKeyUXDigestAndShortKey(t *testing.T) {
	for _, tc := range []struct{ key, digest, short string }{
		{"sha256:" + strings.Repeat("a", 64), strings.Repeat("a", 64), strings.Repeat("a", 12)},
		{strings.Repeat("b", 64), strings.Repeat("b", 64), strings.Repeat("b", 12)},
		{"sha256:abc", "abc", "abc"}, {"sha256:", "", ""}, {"", "", ""},
	} {
		if got := protocol.KeyDigest(tc.key); got != tc.digest {
			t.Errorf("digest(%q)=%q, want %q", tc.key, got, tc.digest)
		}
		if got := protocol.ShortKey(tc.key); got != tc.short {
			t.Errorf("short(%q)=%q, want %q", tc.key, got, tc.short)
		}
	}
}

func TestPairingKeyUXResolveDigestAndSchemePreserveFullIdentity(t *testing.T) {
	key := "sha256:abc123" + strings.Repeat("d", 58)
	s := protocol.PairingSnapshot{Requests: []protocol.PairingRequestView{{IdentityKey: key}, {IdentityKey: "sha256:def456" + strings.Repeat("e", 58)}}}
	for _, typed := range []string{"abc", "sha256:abc", key, strings.TrimPrefix(key, "sha256:"), "  sha256:abc  "} {
		got, err := s.ResolveKey(typed)
		if err != nil || got != key {
			t.Errorf("resolve(%q)=(%q,%v), want full identity", typed, got, err)
		}
	}
	for _, typed := range []string{"", " ", "sha256", "sha256:"} {
		if got, err := s.ResolveKey(typed); got != "" || !errors.Is(err, protocol.ErrPairingEmptyKey) {
			t.Errorf("scheme-only %q resolved: %q %v", typed, got, err)
		}
	}
}

func TestPairingKeyUXAmbiguityAndPartialListsStayConservative(t *testing.T) {
	first := "sha256:" + strings.Repeat("a", 12) + strings.Repeat("b", 52)
	second := "sha256:" + strings.Repeat("a", 12) + strings.Repeat("c", 52)
	s := protocol.PairingSnapshot{Requests: []protocol.PairingRequestView{{IdentityKey: first}, {IdentityKey: second}}}
	for _, typed := range []string{strings.Repeat("a", 12), "sha256:" + strings.Repeat("a", 12)} {
		if _, err := s.ResolveKey(typed); !errors.Is(err, protocol.ErrPairingAmbiguous) {
			t.Fatalf("ambiguous %q: %v", typed, err)
		}
	}
	s.RequestsTruncated = true
	if _, err := s.ResolveKey(strings.Repeat("a", 12) + "b"); !errors.Is(err, protocol.ErrPairingPartial) {
		t.Fatalf("partial unique prefix: %v", err)
	}
	for _, typed := range []string{first, strings.TrimPrefix(first, "sha256:")} {
		if got, err := s.ResolveKey(typed); err != nil || got != first {
			t.Fatalf("full listed identity on partial snapshot: %q %v", got, err)
		}
	}
	s.RequestsTruncated = false
	if _, err := s.ResolveKey("ffff"); !errors.Is(err, protocol.ErrPairingUnknown) {
		t.Fatalf("unknown key: %v", err)
	}
}
