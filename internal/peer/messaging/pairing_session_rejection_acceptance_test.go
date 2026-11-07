package messaging_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestPairingSessionAcceptanceRejectsEpochFramesBelowV9(t *testing.T) {
	id := bytes.Repeat([]byte{5}, 32)
	notice, e := messaging.EncodeContactState(messaging.ContactState{PairingID: id, Gen: 1, State: 1})
	sessionOK(t, e)
	ack, e := messaging.EncodeContactStateStored(messaging.ContactStateStored{PairingID: id, Gen: 1})
	sessionOK(t, e)
	for _, dial := range []bool{true, false} {
		for _, frame := range [][]byte{epochSessionID(t, id), notice, ack} {
			st := epochSessionFixture(t, nil)
			frames := &scriptFrames{input: [][]byte{epochSessionHello(t, 8), frame}}
			_, _, err := epochSessionOpen(sessionSide(frames, st), dial)
			epochSessionError(t, err, messaging.ErrProtocol)
		}
	}
}
func TestPairingSessionAcceptancePairingPhaseRejectsMalformedOrApplicationFrame(t *testing.T) {
	message := messaging.Message{ID: "not-before-pairing", N: 1, Text: "body", CreatedAt: sessionAt}
	app, e := messaging.EncodeRouted(message)
	sessionOK(t, e)
	for _, dial := range []bool{true, false} {
		for _, frame := range [][]byte{consentRawFrame(27, "short"), app} {
			st := epochSessionFixture(t, bytes.Repeat([]byte{6}, 32))
			frames := &scriptFrames{input: [][]byte{epochSessionHello(t, 9), frame}}
			result, _, err := epochSessionOpen(sessionSide(frames, st), dial)
			epochSessionError(t, err, messaging.ErrProtocol)
			if result.Moved() {
				t.Fatal("malformed pairing moved application state")
			}
			sessionEqual(t, len(st.seen), 0)
		}
	}
}
func TestPairingSessionAcceptanceVersionStoreErrorStopsBeforeTraffic(t *testing.T) {
	sentinel := errors.New("version write refused")
	for _, dial := range []bool{true, false} {
		for _, version := range []byte{8, 9} {
			st := epochSessionFixture(t, nil)
			st.sawErr = sentinel
			input := [][]byte{epochSessionHello(t, version)}
			if version == 9 {
				input = append(input, epochSessionID(t, nil))
			}
			frames := &scriptFrames{input: input}
			result, _, err := epochSessionOpen(sessionSide(frames, st), dial)
			epochSessionError(t, err, sentinel)
			var storeErr messaging.StoreError
			if !errors.As(err, &storeErr) {
				t.Fatalf("version failure lost store origin: %v", err)
			}
			if result.Moved() {
				t.Fatal("version failure moved application state")
			}
			expected := 1
			if version == 9 {
				expected = 2
			}
			sessionEqual(t, len(frames.sent), expected)
		}
	}
}

// A store can omit the optional epoch/recorder surfaces and still speak v8.
type epochLegacyStore struct{ messaging.Store }

func TestPairingSessionAcceptanceOldStoreNeedsNoEpochAPI(t *testing.T) {
	db, _ := sessionFixture(t)
	frames := &scriptFrames{input: [][]byte{epochSessionHello(t, 1)}}
	_, version, err := sessionSide(frames, epochLegacyStore{db.PeerMessages()}).OpenDial(context.Background())
	if version != 1 {
		t.Fatalf("legacy version %d", version)
	}
	epochSessionError(t, err, io.EOF)
}
