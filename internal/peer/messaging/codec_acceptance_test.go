package messaging_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

type codecPair[T any] struct {
	encode func(T) ([]byte, error)
	decode func([]byte) (T, error)
	kind   byte
}

func codecRoundTrip[T any](t *testing.T, value T, codec codecPair[T]) {
	t.Helper()
	frame, err := codec.encode(value)
	if err != nil {
		t.Fatal(err)
	}
	gotKind, err := messaging.Kind(frame)
	if err != nil || gotKind != codec.kind {
		t.Fatalf("kind %d: %v", gotKind, err)
	}
	got, err := codec.decode(frame)
	if err != nil || !reflect.DeepEqual(got, value) {
		t.Fatalf("round trip: %+v, %v; want %+v", got, err, value)
	}
	for cut := 0; cut < len(frame); cut++ {
		if _, err := codec.decode(frame[:cut]); !errors.Is(err, messaging.ErrProtocol) {
			t.Fatalf("truncation at %d accepted: %v", cut, err)
		}
	}
	for _, bad := range [][]byte{append(append([]byte{}, frame...), 0), append([]byte{2}, frame[1:]...), append([]byte{1, 255}, frame[2:]...)} {
		if _, err := codec.decode(bad); !errors.Is(err, messaging.ErrProtocol) {
			t.Fatalf("malformed frame accepted: %x (%v)", bad, err)
		}
	}
}

func TestMessageCodecAcceptanceAllFrameKinds(t *testing.T) {
	codecRoundTrip(t, messaging.Hello{Min: 1, Max: 2}, codecPair[messaging.Hello]{messaging.EncodeHello, messaging.DecodeHello, 1})
	codecRoundTrip(t, messaging.Message{ID: "m", N: 42, CreatedAt: time.Date(2026, 10, 28, 0, 0, 0, 123, time.UTC), Text: "hello 世界"}, codecPair[messaging.Message]{messaging.EncodeMessage, messaging.DecodeMessage, 2})
	codecRoundTrip(t, messaging.Stored{ID: "m"}, codecPair[messaging.Stored]{messaging.EncodeStored, messaging.DecodeStored, 3})
	codecRoundTrip(t, messaging.Refused{ID: "m", Refusal: messaging.Refuse("busy")}, codecPair[messaging.Refused]{messaging.EncodeRefused, messaging.DecodeRefused, 4})
	codecRoundTrip(t, messaging.Read{ID: "m"}, codecPair[messaging.Read]{messaging.EncodeRead, messaging.DecodeRead, 6})
	codecRoundTrip(t, messaging.ReadStored{ID: "m"}, codecPair[messaging.ReadStored]{messaging.EncodeReadStored, messaging.DecodeReadStored, 7})
	codecRoundTrip(t, messaging.Unsupported{Min: 2, Max: 3}, codecPair[messaging.Unsupported]{messaging.EncodeUnsupported, messaging.DecodeUnsupported, 8})
}

func TestMessageCodecAcceptanceGoldenWireBytes(t *testing.T) {
	frame, err := messaging.EncodeHello(messaging.Hello{Min: 1, Max: 1})
	if err != nil || hex.EncodeToString(frame) != "0101000101000101" {
		t.Fatalf("hello wire %x, %v", frame, err)
	}
	frame, err = messaging.EncodeStored(messaging.Stored{ID: "m"})
	if err != nil || hex.EncodeToString(frame) != "010300016d" {
		t.Fatalf("stored wire %x, %v", frame, err)
	}
	frame, err = messaging.EncodeEnd()
	if err != nil || !bytes.Equal(frame, []byte{1, 5}) {
		t.Fatalf("end wire %x, %v", frame, err)
	}
	if err = messaging.DecodeEnd(frame); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, {1}, {1, 5, 0}, {1, 3}, {2, 5}} {
		if err = messaging.DecodeEnd(bad); !errors.Is(err, messaging.ErrProtocol) {
			t.Fatalf("bad end accepted %x", bad)
		}
	}
}

func TestMessageCodecAcceptanceRejectsInvalidShapeAndBounds(t *testing.T) {
	for _, frame := range [][]byte{{1, 1, 0, 1, 2, 0, 1, 1}, {1, 1, 0, 0, 0, 1, 1}, {1, 1, 0xff, 0xff}} {
		if _, err := messaging.DecodeHello(frame); !errors.Is(err, messaging.ErrProtocol) {
			t.Fatalf("invalid hello accepted %x", frame)
		}
	}
	frame, err := messaging.EncodeRefused(messaging.Refused{ID: "m", Refusal: messaging.Refusal{Class: "terminal", Reason: "busy"}})
	if err == nil {
		_, err = messaging.DecodeRefused(frame)
	}
	if !errors.Is(err, messaging.ErrProtocol) {
		t.Fatalf("invalid refusal accepted: %v", err)
	}
	if _, err = messaging.Kind(make([]byte, messaging.MaxFrameBytes+1)); !errors.Is(err, messaging.ErrProtocol) {
		t.Fatal("oversized frame accepted")
	}
	if _, err = messaging.EncodeStored(messaging.Stored{ID: strings.Repeat("x", 65536)}); !errors.Is(err, messaging.ErrProtocol) {
		t.Fatal("uint16 overflow accepted")
	}
	if _, err = messaging.EncodeStored(messaging.Stored{ID: strings.Repeat("x", messaging.MaxFrameBytes)}); !errors.Is(err, messaging.ErrProtocol) {
		t.Fatal("frame overflow accepted")
	}
}

func TestMessageCodecAcceptanceHashCoversImmutableFields(t *testing.T) {
	original := messaging.Message{ID: "m", N: 1, CreatedAt: time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), Text: "hello"}
	variants := []messaging.Message{original, original, original, original}
	variants[0].ID = "other"
	variants[1].N++
	variants[2].CreatedAt = original.CreatedAt.Add(time.Nanosecond)
	variants[3].Text = "changed"
	for _, changed := range variants {
		if changed.Hash() == original.Hash() {
			t.Fatalf("hash ignored immutable field: %+v", changed)
		}
	}
	sameInstant := original
	sameInstant.CreatedAt = original.CreatedAt.In(time.FixedZone("offset", 3600))
	if sameInstant.Hash() != original.Hash() {
		t.Fatal("same instant hashes differently across timezones")
	}
	frame, err := messaging.EncodeMessage(sameInstant)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := messaging.DecodeMessage(frame)
	if err != nil || decoded.Hash() != original.Hash() {
		t.Fatalf("wire changed identity: %+v %v", decoded, err)
	}
}
