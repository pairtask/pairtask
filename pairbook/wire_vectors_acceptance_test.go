package pairbook_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"github.com/pairtask/pairtask/pairbook"
	"testing"
	"time"
)

// These literal vectors were produced independently using Python struct.pack,
// from the agreed protocol grammar, without calling the Go encoder.
func TestWireAcceptanceGoldenBytes(t *testing.T) {
	key := ed25519.PublicKey(bytes.Repeat([]byte{0x11}, 32))
	var token, body [32]byte
	var nonce [16]byte
	for i := range token {
		token[i] = 0x22
		body[i] = 0x44
	}
	for i := range nonce {
		nonce[i] = 0x55
	}
	records := map[string]interface{ SigningBytes() []byte }{
		"invite":     pairbook.Invite{Space: "s", TokenHash: token, IssuedAt: 1000, ExpiresAt: 2000},
		"join":       pairbook.JoinRequest{Space: "s", Node: key, Name: "N", TokenHash: token, At: 1000},
		"member":     pairbook.Member{Space: "s", Node: key, Name: "N", Role: pairbook.RoleMember, IssuedAt: 1000, ExpiresAt: 2000},
		"address":    pairbook.Address{Space: "s", Node: key, Addrs: []string{"[::1]:7", "host:8"}, IssuedAt: 1000, ExpiresAt: 2000},
		"revocation": pairbook.Revocation{Space: "s", Node: key, By: bytes.Repeat([]byte{0x33}, 32), At: 1000},
		"http":       pairbook.HTTPAuth{Key: key, Method: "POST", Target: "/v1/s?q=1", BodyHash: body, At: 1000, Nonce: nonce},
	}
	expected := map[string]string{
		"invite":     "0000001270616972626f6f6b2d696e766974652d7631000000017300000020222222222222222222222222222222222222222222222222222222222222222200000000000003e800000000000007d0",
		"join":       "0000001070616972626f6f6b2d6a6f696e2d76310000000173000000201111111111111111111111111111111111111111111111111111111111111111000000014e00000020222222222222222222222222222222222222222222222222222222222222222200000000000003e8",
		"member":     "0000001270616972626f6f6b2d6d656d6265722d76310000000173000000201111111111111111111111111111111111111111111111111111111111111111000000014e000000066d656d62657200000000000003e800000000000007d0",
		"address":    "0000001370616972626f6f6b2d616464726573732d7631000000017300000020111111111111111111111111111111111111111111111111111111111111111100000002000000075b3a3a315d3a3700000006686f73743a3800000000000003e800000000000007d0",
		"revocation": "0000001670616972626f6f6b2d7265766f636174696f6e2d7631000000017300000020111111111111111111111111111111111111111111111111111111111111111100000020333333333333333333333333333333333333333333333333333333333333333300000000000003e8",
		"http":       "0000001070616972626f6f6b2d687474702d763100000020111111111111111111111111111111111111111111111111111111111111111100000004504f5354000000092f76312f733f713d3100000020444444444444444444444444444444444444444444444444444444444444444400000000000003e80000001055555555555555555555555555555555",
	}
	for name, r := range records {
		t.Run(name, func(t *testing.T) {
			want, err := hex.DecodeString(expected[name])
			good(t, err)
			if got := r.SigningBytes(); !bytes.Equal(got, want) {
				t.Fatalf("wire bytes changed: got %x; want %x", got, want)
			}
		})
	}
}

func TestWireAcceptanceSignaturesAndJSONTransport(t *testing.T) {
	owner, node := contractKey("owner"), contractKey("node")
	signed := memberFor(owner, node, "s")
	expected := ed25519.Sign(owner, signed.Member.SigningBytes())
	if !bytes.Equal(expected, signed.Sig) {
		t.Fatal("member helper does not sign the declared wire bytes")
	}
	data, err := json.Marshal(signed)
	good(t, err)
	var received pairbook.SignedMember
	good(t, json.Unmarshal(data, &received))
	good(t, received.Verify("s", pinnedID(public(owner)), contractNow))
	if !bytes.Equal(signed.SigningBytes(), received.SigningBytes()) {
		t.Fatal("JSON transport changed signed payload")
	}
	if got := pairbook.KeyID(public(owner)); got != pinnedID(public(owner)) {
		t.Fatalf("owner fingerprint incompatible: %s", got)
	}
	for _, n := range []int{0, 1, 31, 33, 64} {
		if pairbook.KeyID(make([]byte, n)) != "" {
			t.Fatal("malformed key has fingerprint")
		}
	}
	instant := contractNow.Add(1234567 * time.Nanosecond)
	if got := pairbook.Millis(instant); got != contractNow.UnixMilli()+1 {
		t.Fatalf("time is not Unix milliseconds: %d", got)
	}
}
