package messaging_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"io"
	"testing"
)

func TestMessageSessionAcceptanceLostAckRetainsProgress(t *testing.T) {
	db, _ := sessionFixture(t)
	frames := &scriptFrames{input: [][]byte{sessionHello(t), sessionMessage(t, messaging.Message{ID: "progress", N: 1, CreatedAt: sessionAt, Text: "text"})}, failKind: messaging.FrameStored}
	result, err := sessionSide(frames, db.PeerMessages()).Serve(context.Background())
	sessionErrorIs(t, err, io.ErrUnexpectedEOF)
	sessionEqual(t, result.Received, 1)
	sessionEqual(t, result.Moved(), true)
}

func TestMessageSessionAcceptanceRefusalRetainsProgress(t *testing.T) {
	db, _ := sessionFixture(t)
	_, err := db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: sessionContact, ID: "refused", Text: "text", Now: sessionAt})
	sessionOK(t, err)
	wire, err := messaging.EncodeRefused(messaging.Refused{ID: "refused", Refusal: messaging.Refuse("invalid")})
	sessionOK(t, err)
	frames := &scriptFrames{input: [][]byte{sessionHello(t), wire}}
	result, err := sessionSide(frames, db.PeerMessages()).Dial(context.Background())
	sessionErrorIs(t, err, io.EOF)
	sessionEqual(t, result.Refused, 1)
	sessionEqual(t, result.Moved(), true)
	sessionEqual(t, sessionRows(t, db.PeerMessages(), 1)[0].State, messaging.StateRefused)
}

func TestMessageSessionAcceptanceLostReadAckRetainsProgress(t *testing.T) {
	db, _ := sessionFixture(t)
	ctx := context.Background()
	_, err := db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: sessionContact, ID: "read-progress", Text: "text", Now: sessionAt})
	sessionOK(t, err)
	sessionOK(t, db.PeerMessages().Delivered(ctx, sessionContact, "read-progress", sessionAt))
	end, err := messaging.EncodeEnd()
	sessionOK(t, err)
	read, err := messaging.EncodeRead(messaging.Read{ID: "read-progress"})
	sessionOK(t, err)
	frames := &scriptFrames{input: [][]byte{sessionHello(t), end, read}, failKind: messaging.FrameReadStored}
	result, err := sessionSide(frames, db.PeerMessages()).Serve(ctx)
	sessionErrorIs(t, err, io.ErrUnexpectedEOF)
	sessionEqual(t, result.ReadsStored, 1)
	sessionEqual(t, result.Moved(), true)
	sessionEqual(t, sessionRows(t, db.PeerMessages(), 1)[0].State, messaging.StateRead)
}
