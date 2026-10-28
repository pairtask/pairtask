package messaging_test

import (
	"context"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"io"
	"testing"
)

func TestMessageSessionAcceptanceLostAckRetainsProgress(t *testing.T) {
	db, _ := sessionFixture(t)
	frames := &scriptFrames{input: [][]byte{sessionHello(t), sessionMessage(t, messaging.Message{ID: "progress", N: 1, CreatedAt: sessionAt, Text: "text"})}, failKind: messaging.FrameStored}
	result, err := sessionSide(frames, db.PeerMessages()).Serve(context.Background())
	if !errors.Is(err, io.ErrUnexpectedEOF) || result.Received != 1 || !result.Moved() {
		t.Fatalf("lost durable progress: %+v %v", result, err)
	}
}

func TestMessageSessionAcceptanceRefusalRetainsProgress(t *testing.T) {
	db, _ := sessionFixture(t)
	_, err := db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: sessionContact, ID: "refused", Text: "text", Now: sessionAt})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := messaging.EncodeRefused(messaging.Refused{ID: "refused", Refusal: messaging.Refuse("invalid")})
	if err != nil {
		t.Fatal(err)
	}
	frames := &scriptFrames{input: [][]byte{sessionHello(t), wire}}
	result, err := sessionSide(frames, db.PeerMessages()).Dial(context.Background())
	if !errors.Is(err, io.EOF) || result.Refused != 1 || !result.Moved() {
		t.Fatalf("lost refusal progress: %+v %v", result, err)
	}
	rows, _, err := db.PeerMessages().Page(context.Background(), sessionContact, 0, 64)
	if err != nil || len(rows) != 1 || rows[0].State != messaging.StateRefused {
		t.Fatalf("refusal not durable: %+v %v", rows, err)
	}
}

func TestMessageSessionAcceptanceLostReadAckRetainsProgress(t *testing.T) {
	db, _ := sessionFixture(t)
	ctx := context.Background()
	_, err := db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: sessionContact, ID: "read-progress", Text: "text", Now: sessionAt})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.PeerMessages().Delivered(ctx, sessionContact, "read-progress", sessionAt); err != nil {
		t.Fatal(err)
	}
	end, err := messaging.EncodeEnd()
	if err != nil {
		t.Fatal(err)
	}
	read, err := messaging.EncodeRead(messaging.Read{ID: "read-progress"})
	if err != nil {
		t.Fatal(err)
	}
	frames := &scriptFrames{input: [][]byte{sessionHello(t), end, read}, failKind: messaging.FrameReadStored}
	result, err := sessionSide(frames, db.PeerMessages()).Serve(ctx)
	if !errors.Is(err, io.ErrUnexpectedEOF) || result.ReadsStored != 1 || !result.Moved() {
		t.Fatalf("lost read progress: %+v %v", result, err)
	}
	rows, _, err := db.PeerMessages().Page(ctx, sessionContact, 0, 64)
	if err != nil || len(rows) != 1 || rows[0].State != messaging.StateRead {
		t.Fatalf("read not durable: %+v %v", rows, err)
	}
}
