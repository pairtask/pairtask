package messaging_test

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

var sessionAt = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

const sessionContact = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type scriptFrames struct {
	input    [][]byte
	sent     [][]byte
	failKind byte
}

func (f *scriptFrames) Send(b []byte) error {
	kind, _ := messaging.Kind(b)
	if kind == f.failKind {
		return io.ErrUnexpectedEOF
	}
	f.sent = append(f.sent, append([]byte{}, b...))
	return nil
}
func (f *scriptFrames) Recv() ([]byte, error) {
	if len(f.input) == 0 {
		return nil, io.EOF
	}
	b := f.input[0]
	f.input = f.input[1:]
	return b, nil
}
func sessionFrame(t *testing.T, b []byte, err error) []byte {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func sessionHello(t *testing.T) []byte {
	t.Helper()
	b, err := messaging.EncodeHello(messaging.Hello{Min: 1, Max: 1})
	return sessionFrame(t, b, err)
}
func sessionMessage(t *testing.T, m messaging.Message) []byte {
	t.Helper()
	b, err := messaging.EncodeMessage(m)
	return sessionFrame(t, b, err)
}
func sessionDB(t *testing.T, path string) *store.Store {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func sessionFixture(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "messages.db")
	db := sessionDB(t, path)
	outcome := peer.PairingOutcome{OperationID: "operation", InvitationID: "invitation", IdentityKey: sessionContact, Name: "Peer", TranscriptID: make([]byte, 64), At: sessionAt}
	if _, err := db.Commit(context.Background(), outcome); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Accept(context.Background(), sessionContact); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Deliver(context.Background(), sessionContact, peer.PairingAccepted, sessionAt); err != nil {
		t.Fatal(err)
	}
	return db, path
}
func sessionSide(f messaging.Frames, s messaging.Store) messaging.Session {
	return messaging.Session{Frames: f, Store: s, Contact: sessionContact, Now: func() time.Time { return sessionAt }}
}

func TestMessageSessionAcceptanceRejectsMalformedUnsupported(t *testing.T) {
	for _, frame := range [][]byte{{1, 8}, {1, 8, 0, 1, 1}, {1, 8, 0, 1, 2, 0, 1, 1}} {
		frames := &scriptFrames{input: [][]byte{frame}}
		_, err := sessionSide(frames, nil).Dial(context.Background())
		if !errors.Is(err, messaging.ErrProtocol) {
			t.Fatalf("malformed Unsupported classified as %v", err)
		}
	}
}

func TestMessageSessionAcceptanceNegotiatesUnsupportedBeforeStore(t *testing.T) {
	hello, err := messaging.EncodeHello(messaging.Hello{Min: 2, Max: 3})
	if err != nil {
		t.Fatal(err)
	}
	frames := &scriptFrames{input: [][]byte{hello}}
	_, err = sessionSide(frames, nil).Serve(context.Background())
	if !errors.Is(err, messaging.ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
	if len(frames.sent) != 1 {
		t.Fatalf("expected one unsupported answer, got %d", len(frames.sent))
	}
	if _, err := messaging.DecodeUnsupported(frames.sent[0]); err != nil {
		t.Fatal(err)
	}
}

func sessionOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func sessionEqual[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
}
func sessionErrorIs(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("got error %v want %v", got, want)
	}
}

func TestMessageSessionAcceptanceLostStoredRetriesAfterRestart(t *testing.T) {
	db, path := sessionFixture(t)
	m := messaging.Message{ID: "lost-ack", N: 1, CreatedAt: sessionAt, Text: "one durable message"}
	frames := &scriptFrames{input: [][]byte{sessionHello(t), sessionMessage(t, m)}, failKind: messaging.FrameStored}
	_, err := sessionSide(frames, db.PeerMessages()).Serve(context.Background())
	sessionErrorIs(t, err, io.ErrUnexpectedEOF)
	sessionOK(t, db.Close())
	db = sessionDB(t, path)
	retry := &scriptFrames{input: [][]byte{sessionHello(t), sessionMessage(t, m)}}
	_, err = sessionSide(retry, db.PeerMessages()).Serve(context.Background())
	sessionErrorIs(t, err, io.EOF)
	sessionEqual(t, len(retry.sent), 2)
	ack, err := messaging.DecodeStored(retry.sent[1])
	sessionOK(t, err)
	sessionEqual(t, ack.ID, m.ID)
	sessionEqual(t, sessionRows(t, db.PeerMessages(), 1)[0].Text, m.Text)
}

func TestMessageSessionAcceptanceWrongStoredDoesNotDeliver(t *testing.T) {
	db, _ := sessionFixture(t)
	if _, err := db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: sessionContact, ID: "wanted", Text: "text", Now: sessionAt}); err != nil {
		t.Fatal(err)
	}
	ack, err := messaging.EncodeStored(messaging.Stored{ID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	frames := &scriptFrames{input: [][]byte{sessionHello(t), ack}}
	_, err = sessionSide(frames, db.PeerMessages()).Dial(context.Background())
	if !errors.Is(err, messaging.ErrProtocol) {
		t.Fatalf("wrong ID accepted: %v", err)
	}
	rows, _, err := db.PeerMessages().Page(context.Background(), sessionContact, 0, 64)
	if err != nil || len(rows) != 1 || rows[0].State != messaging.StateQueued {
		t.Fatalf("wrong ID changed delivery: %+v %v", rows, err)
	}
}

type failingAcceptStore struct{ messaging.Store }

func (failingAcceptStore) Accept(context.Context, string, messaging.Message, time.Time) (messaging.Answer, error) {
	return messaging.Answer{Stored: true}, io.ErrClosedPipe
}
func TestMessageSessionAcceptanceStoreErrorNeverAnswersStored(t *testing.T) {
	m := messaging.Message{ID: "write-fails", N: 1, CreatedAt: sessionAt, Text: "text"}
	frames := &scriptFrames{input: [][]byte{sessionHello(t), sessionMessage(t, m)}}
	_, err := sessionSide(frames, failingAcceptStore{}).Serve(context.Background())
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("store error lost: %v", err)
	}
	var storeError messaging.StoreError
	if !errors.As(err, &storeError) {
		t.Fatalf("store origin lost: %v", err)
	}
	if len(frames.sent) != 1 {
		t.Fatalf("store failure sent extra answer: %x", frames.sent)
	}
}
