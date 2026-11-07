package messaging_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestControlSessionAcceptanceMismatchOnlyExchangesEnd(t *testing.T) {
	for _, dial := range []bool{true, false} {
		p := &controlProbe{id: bytes.Repeat([]byte{1}, 32)}
		frames := &scriptFrames{input: [][]byte{epochSessionID(t, bytes.Repeat([]byte{2}, 32)), controlEnd(t)}}
		result, _ := controlSessionOpen(frames, p, dial)
		if result.Sent || result.Received {
			t.Fatal("different pairing moved a notice")
		}
		if !reflect.DeepEqual(p.calls, []string{"id"}) {
			t.Fatalf("mismatch used notice store: %v", p.calls)
		}
		if !reflect.DeepEqual(epochSessionKinds(t, frames), []byte{27, messaging.FrameEnd}) {
			t.Fatalf("mismatch framing: %x", frames.sent)
		}
	}
}
func TestControlSessionAcceptanceRemoteUnpairSendsOnlyAckThenCloses(t *testing.T) {
	for _, dial := range []bool{true, false} {
		t.Run(map[bool]string{true: "dial", false: "serve"}[dial], func(t *testing.T) {
			id := bytes.Repeat([]byte{3}, 32)
			incoming := messaging.ContactState{PairingID: id, Gen: 17, State: messaging.ContactStateUnpaired}
			own := messaging.ContactState{PairingID: id, Gen: 12, State: messaging.ContactStatePaused}
			p := &controlProbe{id: id, closeOnAccept: true}
			input := [][]byte{epochSessionID(t, id)}
			if dial {
				input = append(input, controlNoticeFrame(t, incoming))
			} else {
				p.pending = &own
				input = append(input, controlNoticeFrame(t, incoming))
			}
			frames := &scriptFrames{input: input}
			result, err := controlSessionOpen(frames, p, dial)
			sessionOK(t, err)
			sessionEqual(t, result.Received, true)
			sessionEqual(t, result.ClosedByUnpair, true)
			expected := []byte{27, 29}
			if dial {
				expected = []byte{27, messaging.FrameEnd, 29}
			}
			if !reflect.DeepEqual(epochSessionKinds(t, frames), expected) {
				t.Fatalf("traffic after terminal ACK: %x", frames.sent)
			}
			ack, e := messaging.DecodeContactStateStored(frames.sent[len(frames.sent)-1])
			sessionOK(t, e)
			sessionEqual(t, ack.Gen, incoming.Gen)
			if !bytes.Equal(ack.PairingID, id) {
				t.Fatal("wrong pairing ID in terminal ACK")
			}
			sessionEqual(t, p.invalidated, true)
			controlClosedCalls(t, p, dial)
		})
	}
}
func TestControlSessionAcceptanceEmptySessionHasNoHello(t *testing.T) {
	for _, dial := range []bool{true, false} {
		p := &controlProbe{}
		frames := &scriptFrames{input: [][]byte{epochSessionID(t, nil), controlEnd(t)}}
		result, err := controlSessionOpen(frames, p, dial)
		sessionOK(t, err)
		moved := result.Sent || result.Received || result.ClosedByUnpair
		if moved {
			t.Fatal("empty session moved notices")
		}
		if !reflect.DeepEqual(epochSessionKinds(t, frames), []byte{27, messaging.FrameEnd}) {
			t.Fatalf("unexpected control framing: %x", frames.sent)
		}
	}
}
func TestControlSessionAcceptanceRejectsHelloAndApplicationFrames(t *testing.T) {
	for _, dial := range []bool{true, false} {
		for _, bad := range [][]byte{epochSessionHello(t, 9), sessionMessage(t, messaging.Message{ID: "application", N: 1, Text: "no", CreatedAt: sessionAt})} {
			p := &controlProbe{}
			frames := &scriptFrames{input: [][]byte{bad}}
			_, err := controlSessionOpen(frames, p, dial)
			epochSessionError(t, err, messaging.ErrProtocol)
			sessionEqual(t, len(p.accepted), 0)
			sessionEqual(t, len(p.stored), 0)
		}
	}
}
func TestControlSessionAcceptanceRejectsOutOfOrderAck(t *testing.T) {
	for _, dial := range []bool{true, false} {
		n := messaging.ContactState{Gen: 1, State: messaging.ContactStatePaused}
		frames := &scriptFrames{input: [][]byte{epochSessionID(t, nil), controlAckFrame(t, n)}}
		p := &controlProbe{}
		_, err := controlSessionOpen(frames, p, dial)
		epochSessionError(t, err, messaging.ErrProtocol)
		sessionEqual(t, len(p.accepted), 0)
	}
}

func TestControlSessionAcceptanceBothDirectionsStoreExactNotices(t *testing.T) {
	for _, dial := range []bool{true, false} {
		id := bytes.Repeat([]byte{4}, 32)
		own := messaging.ContactState{PairingID: id, Gen: 9, State: messaging.ContactStatePaused}
		remote := messaging.ContactState{PairingID: id, Gen: 11, State: messaging.ContactStateResumed}
		p := &controlProbe{id: id, pending: &own}
		input := [][]byte{epochSessionID(t, id), controlNoticeFrame(t, remote), controlAckFrame(t, own)}
		expected := []byte{27, 29, 28}
		if dial {
			input = [][]byte{epochSessionID(t, id), controlAckFrame(t, own), controlNoticeFrame(t, remote)}
			expected = []byte{27, 28, 29}
		}
		frames := &scriptFrames{input: input}
		result, err := controlSessionOpen(frames, p, dial)
		sessionOK(t, err)
		sessionEqual(t, result.Sent, true)
		sessionEqual(t, result.Received, true)
		if !reflect.DeepEqual(epochSessionKinds(t, frames), expected) {
			t.Fatalf("control ordering %x", frames.sent)
		}
		if !reflect.DeepEqual(p.accepted, []messaging.ContactState{remote}) {
			t.Fatalf("accepted %v", p.accepted)
		}
		sessionEqual(t, len(p.stored), 1)
		sessionEqual(t, p.stored[0].Gen, own.Gen)
	}
}
