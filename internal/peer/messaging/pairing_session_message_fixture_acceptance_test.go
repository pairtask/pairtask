package messaging_test

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func epochSessionMessageFixture(t *testing.T, dial bool) (*epochSessionStore, *scriptFrames) {
	t.Helper()
	db, _ := sessionFixture(t)
	if dial {
		_, err := db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: sessionContact, ID: "epoch-message", Text: "body", Now: sessionAt})
		sessionOK(t, err)
	}
	bound, err := db.PeerMessages().Admit(context.Background(), sessionContact)
	sessionOK(t, err)
	st := &epochSessionStore{PeerMessages: bound, id: bytes.Repeat([]byte{3}, 32)}
	next := epochSessionMessageAnswer(t, dial)
	frames := &scriptFrames{input: [][]byte{epochSessionHello(t, 9), epochSessionID(t, st.id), next}}
	return st, frames
}
func epochSessionMessageAnswer(t *testing.T, dial bool) []byte {
	t.Helper()
	if dial {
		b, e := messaging.EncodeStored(messaging.Stored{ID: "epoch-message"})
		return sessionFrame(t, b, e)
	}
	m := messaging.Message{ID: "epoch-message", N: 1, Text: "body", CreatedAt: sessionAt}
	b, e := messaging.EncodeRouted(m)
	return sessionFrame(t, b, e)
}
func epochSessionCheckPrefix(t *testing.T, frames *scriptFrames, id []byte) {
	t.Helper()
	kinds := epochSessionKinds(t, frames)
	if len(kinds) < 3 {
		t.Fatalf("missing application traffic: %v", kinds)
	}
	want := []byte{messaging.FrameHello, messaging.FramePairingEpoch}
	if !reflect.DeepEqual(kinds[:2], want) {
		t.Fatalf("pairing not first: %v", kinds)
	}
	got, err := messaging.DecodePairingEpoch(frames.sent[1])
	sessionOK(t, err)
	if !bytes.Equal(got.PairingID, id) {
		t.Fatal("wrong local pairing ID sent")
	}
}
