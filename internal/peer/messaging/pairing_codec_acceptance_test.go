package messaging_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

// Build expected wire bytes independently of the production encoder.
func epochWire(kind byte, fields ...[]byte) []byte {
	out := []byte{1, kind}
	for _, field := range fields {
		out = binary.BigEndian.AppendUint16(out, uint16(len(field)))
		out = append(out, field...)
	}
	return out
}

func epochProtocolError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, messaging.ErrProtocol) {
		t.Fatalf("want ErrProtocol, got %v", err)
	}
}

func epochBytes(t *testing.T, got, want []byte, err error) {
	t.Helper()
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("wire got %x, want %x: %v", got, want, err)
	}
}

func TestPairingCodecAcceptanceWireConstants(t *testing.T) {
	got := []byte{messaging.MaxVersion, messaging.PairingVersion, messaging.FramePairingEpoch,
		messaging.FrameContactState, messaging.FrameContactStateStored, messaging.PairingIDBytes,
		messaging.ContactStatePaused, messaging.ContactStateResumed, messaging.ContactStateUnpaired}
	if !bytes.Equal(got, []byte{10, 9, 27, 28, 29, 32, 1, 2, 3}) {
		t.Fatalf("wire constants: %v", got)
	}
}

func TestPairingCodecAcceptanceEpochWireAndRoundTrip(t *testing.T) {
	for _, id := range [][]byte{nil, {}, bytes.Repeat([]byte{0xa5}, 32)} {
		t.Run(fmt.Sprintf("length-%d-nil-%t", len(id), id == nil), func(t *testing.T) {
			wire, err := messaging.EncodePairingEpoch(messaging.PairingEpoch{PairingID: id})
			epochBytes(t, wire, epochWire(27, id), err)
			got, err := messaging.DecodePairingEpoch(wire)
			epochBytes(t, got.PairingID, id, err)
			kind, err := messaging.Kind(wire)
			if err != nil || kind != 27 {
				t.Fatalf("Kind=%d: %v", kind, err)
			}
		})
	}
}

func epochStateRoundTrip(t *testing.T, id []byte, gen uint64, state byte) {
	t.Helper()
	generation := binary.BigEndian.AppendUint64(nil, gen)
	wire, err := messaging.EncodeContactState(messaging.ContactState{PairingID: id, Gen: gen, State: state})
	epochBytes(t, wire, epochWire(28, id, generation, []byte{state}), err)
	got, err := messaging.DecodeContactState(wire)
	epochBytes(t, got.PairingID, id, err)
	if got.Gen != gen || got.State != state {
		t.Fatalf("notice changed: %+v", got)
	}
	wire, err = messaging.EncodeContactStateStored(messaging.ContactStateStored{PairingID: id, Gen: gen})
	epochBytes(t, wire, epochWire(29, id, generation), err)
	stored, err := messaging.DecodeContactStateStored(wire)
	epochBytes(t, stored.PairingID, id, err)
	if stored.Gen != gen {
		t.Fatalf("stored generation=%d, want %d", stored.Gen, gen)
	}
}

func TestPairingCodecAcceptanceNoticesPreserveIDGenerationAndState(t *testing.T) {
	for _, id := range [][]byte{nil, bytes.Repeat([]byte{0xff}, 32)} {
		for _, gen := range []uint64{1, 1 << 63, ^uint64(0)} {
			for _, state := range []byte{1, 2, 3} {
				t.Run(fmt.Sprintf("id-%d-gen-%d-state-%d", len(id), gen, state), func(t *testing.T) {
					epochStateRoundTrip(t, id, gen, state)
				})
			}
		}
	}
}

func TestPairingCodecAcceptanceRejectsInvalidValues(t *testing.T) {
	for _, n := range []int{1, 31, 33, 65536} {
		t.Run(fmt.Sprintf("id-%d", n), func(t *testing.T) {
			id := make([]byte, n)
			_, err := messaging.EncodePairingEpoch(messaging.PairingEpoch{PairingID: id})
			epochProtocolError(t, err)
			_, err = messaging.EncodeContactState(messaging.ContactState{PairingID: id, Gen: 1, State: 1})
			epochProtocolError(t, err)
			_, err = messaging.EncodeContactStateStored(messaging.ContactStateStored{PairingID: id, Gen: 1})
			epochProtocolError(t, err)
		})
	}
	for _, state := range []byte{0, 4, 255} {
		_, err := messaging.EncodeContactState(messaging.ContactState{Gen: 1, State: state})
		epochProtocolError(t, err)
	}
	_, err := messaging.EncodeContactState(messaging.ContactState{State: 1})
	epochProtocolError(t, err)
	_, err = messaging.EncodeContactStateStored(messaging.ContactStateStored{})
	epochProtocolError(t, err)
}

type epochDecoder struct {
	kind   byte
	fields [][]byte
	decode func([]byte) error
}

func epochDecoders() []epochDecoder {
	gen := binary.BigEndian.AppendUint64(nil, 1)
	return []epochDecoder{
		{27, [][]byte{make([]byte, 32)}, func(b []byte) error { _, err := messaging.DecodePairingEpoch(b); return err }},
		{28, [][]byte{make([]byte, 32), gen, {1}}, func(b []byte) error { _, err := messaging.DecodeContactState(b); return err }},
		{29, [][]byte{make([]byte, 32), gen}, func(b []byte) error { _, err := messaging.DecodeContactStateStored(b); return err }},
	}
}

func epochMalformedFrames(c epochDecoder) [][]byte {
	wire := epochWire(c.kind, c.fields...)
	wrongVersion := append([]byte{}, wire...)
	wrongVersion[0] = 2
	wrongKind := append([]byte{}, wire...)
	wrongKind[1] = 0
	frames := [][]byte{nil, {1}, append(append([]byte{}, wire...), 0), wrongVersion, wrongKind, make([]byte, messaging.MaxFrameBytes+1)}
	for n := 0; n < len(wire); n++ {
		frames = append(frames, wire[:n])
	}
	for _, n := range []int{1, 31, 33} {
		fields := append([][]byte{}, c.fields...)
		fields[0] = make([]byte, n)
		frames = append(frames, epochWire(c.kind, fields...))
	}
	return frames
}

func TestPairingCodecAcceptanceRejectsMalformedFrames(t *testing.T) {
	for _, c := range epochDecoders() {
		t.Run(fmt.Sprintf("kind-%d", c.kind), func(t *testing.T) {
			kind, err := messaging.Kind(epochWire(c.kind, c.fields...))
			if err != nil || kind != c.kind {
				t.Errorf("Kind=%d want %d: %v", kind, c.kind, err)
			}
			for n, frame := range epochMalformedFrames(c) {
				t.Run(fmt.Sprintf("mutation-%d", n), func(t *testing.T) { epochProtocolError(t, c.decode(frame)) })
			}
			epochRejectOtherKinds(t, c)
		})
	}
}

func TestPairingCodecAcceptanceRejectsMalformedNoticeFields(t *testing.T) {
	id := make([]byte, 32)
	for _, generation := range [][]byte{nil, {1}, make([]byte, 7), make([]byte, 8), make([]byte, 9)} {
		_, err := messaging.DecodeContactState(epochWire(28, id, generation, []byte{1}))
		epochProtocolError(t, err)
		_, err = messaging.DecodeContactStateStored(epochWire(29, id, generation))
		epochProtocolError(t, err)
	}
	gen := binary.BigEndian.AppendUint64(nil, 1)
	for _, state := range [][]byte{nil, {0}, {4}, {255}, {1, 2}} {
		_, err := messaging.DecodeContactState(epochWire(28, id, gen, state))
		epochProtocolError(t, err)
	}
}

func epochRejectOtherKinds(t *testing.T, c epochDecoder) {
	t.Helper()
	for _, other := range epochDecoders() {
		if other.kind != c.kind {
			epochProtocolError(t, c.decode(epochWire(other.kind, other.fields...)))
		}
	}
}
