package messaging_test

import (
	"bytes"
	"io"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestPairingSessionAcceptanceRejectsAnotherPairingBeforeTraffic(t *testing.T) {
	for _, dial := range []bool{true, false} {
		for _, ids := range []struct {
			name          string
			local, remote []byte
		}{
			{"different", bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)},
			{"local-legacy", nil, bytes.Repeat([]byte{2}, 32)},
			{"remote-legacy", bytes.Repeat([]byte{1}, 32), nil},
		} {
			t.Run(ids.name+map[bool]string{true: "/dial", false: "/serve"}[dial], func(t *testing.T) {
				st := epochSessionFixture(t, ids.local)
				frames := &scriptFrames{input: [][]byte{epochSessionHello(t, 9), epochSessionID(t, ids.remote)}}
				result, _, err := epochSessionOpen(sessionSide(frames, st), dial)
				epochSessionError(t, err, messaging.ErrOtherPairing)
				if result.Moved() {
					t.Fatal("mismatched session moved application state")
				}
				if !reflect.DeepEqual(epochSessionKinds(t, frames), []byte{messaging.FrameHello, messaging.FramePairingEpoch}) {
					t.Fatalf("application traffic before pairing agreement: %x", frames.sent)
				}
				sessionEqual(t, len(st.seen), 0)
			})
		}
	}
}
func TestPairingSessionAcceptanceMatchingIDBeforeMessage(t *testing.T) {
	for _, dial := range []bool{true, false} {
		t.Run(map[bool]string{true: "dial", false: "serve"}[dial], func(t *testing.T) {
			st, frames := epochSessionMessageFixture(t, dial)
			result, version, err := epochSessionOpen(sessionSide(frames, st), dial)
			epochSessionError(t, err, io.EOF)
			sessionEqual(t, version, byte(9))
			epochSessionCheckPrefix(t, frames, st.id)
			if !reflect.DeepEqual(st.seen, []byte{9}) {
				t.Fatalf("SawVersion calls: %v", st.seen)
			}
			if dial {
				sessionEqual(t, result.Sent, 1)
			} else {
				sessionEqual(t, result.Received, 1)
			}
		})
	}
}
func TestPairingSessionAcceptanceLegacyV9ExchangesEmptyID(t *testing.T) {
	for _, dial := range []bool{true, false} {
		st := epochSessionFixture(t, nil)
		frames := &scriptFrames{input: [][]byte{epochSessionHello(t, 9), epochSessionID(t, nil)}}
		_, version, err := epochSessionOpen(sessionSide(frames, st), dial)
		epochSessionError(t, err, io.EOF)
		sessionEqual(t, version, byte(9))
		if len(frames.sent) < 2 {
			t.Fatal("missing empty pairing ID frame")
		}
		got, e := messaging.DecodePairingEpoch(frames.sent[1])
		sessionOK(t, e)
		sessionEqual(t, len(got.PairingID), 0)
		if !reflect.DeepEqual(st.seen, []byte{9}) {
			t.Fatalf("legacy v9 SawVersion calls %v", st.seen)
		}
	}
}
func TestPairingSessionAcceptanceLegacyV8HasNoEpochFrame(t *testing.T) {
	for _, dial := range []bool{true, false} {
		st := epochSessionFixture(t, nil)
		frames := &scriptFrames{input: [][]byte{epochSessionHello(t, 8)}}
		_, version, err := epochSessionOpen(sessionSide(frames, st), dial)
		epochSessionError(t, err, io.EOF)
		sessionEqual(t, version, byte(8))
		h, e := messaging.DecodeHello(frames.sent[0])
		sessionOK(t, e)
		sessionEqual(t, h.Min, byte(1))
		for _, k := range epochSessionKinds(t, frames) {
			if k >= 27 && k <= 29 {
				t.Fatalf("v8 received pairing frame %d", k)
			}
		}
		if !reflect.DeepEqual(st.seen, []byte{8}) {
			t.Fatalf("legacy SawVersion calls %v", st.seen)
		}
	}
}
func TestPairingSessionAcceptanceEpochRefusesV8(t *testing.T) {
	for _, dial := range []bool{true, false} {
		st := epochSessionFixture(t, bytes.Repeat([]byte{4}, 32))
		frames := &scriptFrames{input: [][]byte{epochSessionHello(t, 8)}}
		_, _, err := epochSessionOpen(sessionSide(frames, st), dial)
		epochSessionError(t, err, messaging.ErrUnsupported)
		sessionEqual(t, len(frames.sent), 1)
		if dial {
			h, e := messaging.DecodeHello(frames.sent[0])
			sessionOK(t, e)
			sessionEqual(t, h.Min, byte(9))
		} else {
			u, e := messaging.DecodeUnsupported(frames.sent[0])
			sessionOK(t, e)
			sessionEqual(t, u.Min, byte(9))
		}
		sessionEqual(t, len(st.seen), 0)
	}
}
