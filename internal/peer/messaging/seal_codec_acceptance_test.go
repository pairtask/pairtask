package messaging_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestSealCodecAcceptancePreservesEachMessageKindAndOpaqueSeal(t *testing.T) {
	base := messaging.Message{ID: "sealed-1", N: 1, CreatedAt: sessionAt, Text: "signed across workspaces"}
	routed := base
	routed.From = "agent-1"
	routed.To = []string{"remote-1"}
	routed.ReplyTo = messaging.ReplyRef{ID: "parent", Author: messaging.ReplyByReceiver}
	review := routed
	review.ReplyTo = messaging.ReplyRef{}
	review.Review = &messaging.ReviewAsk{Forge: "github", Repository: "owner/repo", Change: "1", Base: "base", Head: "head"}

	cases := []struct {
		name string
		make func() ([]byte, error)
	}{
		{"plain", func() ([]byte, error) { return messaging.EncodeMessage(base) }},
		{"routed", func() ([]byte, error) { return messaging.EncodeRouted(routed) }},
		{"review", func() ([]byte, error) { return messaging.EncodeReviewAsk(review) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner, err := tc.make()
			if err != nil {
				t.Fatal(err)
			}
			seal := []byte{0, 255, 1, 0, 42}
			wire, err := messaging.EncodeSealed(inner, seal)
			if err != nil {
				t.Fatal(err)
			}
			if kind, err := messaging.Kind(wire); err != nil || kind != messaging.FrameSealed {
				t.Fatalf("outer frame kind=%d: %v", kind, err)
			}
			gotInner, gotSeal, err := messaging.DecodeSealed(wire)
			if err != nil || !bytes.Equal(gotInner, inner) || !bytes.Equal(gotSeal, seal) {
				t.Fatalf("sealed round trip: inner=%x seal=%x err=%v", gotInner, gotSeal, err)
			}
			// A pre-v10 decoder must not mistake the wrapper for an unsigned message.
			if _, err := messaging.DecodeMessage(wire); !errors.Is(err, messaging.ErrProtocol) {
				t.Fatalf("legacy message decoder accepted sealed frame: %v", err)
			}
		})
	}
	inner, err := messaging.EncodeMessage(base)
	if err != nil {
		t.Fatal(err)
	}
	unsigned, err := messaging.EncodeSealed(inner, nil)
	if err != nil {
		t.Fatal(err)
	}
	gotInner, gotSeal, err := messaging.DecodeSealed(unsigned)
	if err != nil || !bytes.Equal(gotInner, inner) || len(gotSeal) != 0 {
		t.Fatalf("unsigned message changed: inner=%x seal=%x err=%v", gotInner, gotSeal, err)
	}
}

func TestSealCodecAcceptanceRejectsControlNestingAndMalformedFrames(t *testing.T) {
	message, err := messaging.EncodeMessage(messaging.Message{ID: "m", N: 1, CreatedAt: sessionAt, Text: "body"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := messaging.EncodeStored(messaging.Stored{ID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	hello, err := messaging.EncodeHello(messaging.Hello{Min: 1, Max: messaging.MaxVersion})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := messaging.EncodeSealed(message, []byte("seal"))
	if err != nil {
		t.Fatal(err)
	}
	for name, inner := range map[string][]byte{
		"stored":    stored,
		"hello":     hello,
		"nested":    valid,
		"unknown":   {1, 31},
		"malformed": {1, messaging.FrameMessage, 0},
	} {
		t.Run(name, func(t *testing.T) {
			outer, err := messaging.EncodeSealed(inner, []byte("seal"))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := messaging.DecodeSealed(outer); !errors.Is(err, messaging.ErrProtocol) {
				t.Fatalf("invalid inner frame accepted: %v", err)
			}
		})
	}
	for name, wire := range map[string][]byte{
		"truncated":     valid[:len(valid)-1],
		"trailing":      append(bytes.Clone(valid), 0),
		"wrong version": append([]byte{2}, valid[1:]...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := messaging.DecodeSealed(wire); !errors.Is(err, messaging.ErrProtocol) {
				t.Fatalf("malformed outer frame accepted: %v", err)
			}
		})
	}
	if _, err := messaging.EncodeSealed(message, bytes.Repeat([]byte{1}, messaging.MaxSealBytes+1)); !errors.Is(err, messaging.ErrProtocol) {
		t.Fatalf("oversized seal accepted: %v", err)
	}
	limit, err := messaging.EncodeSealed(message, bytes.Repeat([]byte{1}, messaging.MaxSealBytes))
	if err != nil {
		t.Fatalf("maximum seal rejected: %v", err)
	}
	if _, got, err := messaging.DecodeSealed(limit); err != nil || len(got) != messaging.MaxSealBytes {
		t.Fatalf("maximum seal changed: len=%d err=%v", len(got), err)
	}
}

func TestSealCodecAcceptanceSealDoesNotChangeMessageIdentity(t *testing.T) {
	m := routedFixture()
	hash := m.Hash()
	m.Seal = []byte("one signing attempt")
	if m.Hash() != hash {
		t.Fatal("seal changed retry identity")
	}
	m.Seal = []byte("a different signature")
	if m.Hash() != hash {
		t.Fatal("changing the seal changed retry identity")
	}
}
