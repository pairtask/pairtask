package messaging_test

import (
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

type directoryReceiptFrames struct {
	*scriptFrames
	directoryReceived bool
	loseReceipt       bool
}

func (f *directoryReceiptFrames) Recv() ([]byte, error) {
	b, err := f.scriptFrames.Recv()
	kind, _ := messaging.Kind(b)
	if kind == messaging.FrameDirectory {
		f.directoryReceived = true
	}
	return b, err
}

func (f *directoryReceiptFrames) Send(b []byte) error {
	kind, _ := messaging.Kind(b)
	if f.loseReceipt && f.directoryReceived && kind == messaging.FrameEnd {
		return io.ErrUnexpectedEOF
	}
	return f.scriptFrames.Send(b)
}

func directoryRoundFrames(t *testing.T, agents []string) *directoryReceiptFrames {
	t.Helper()
	directory, err := messaging.EncodeDirectory(agents)
	input := append(routingSessionPrefix(t), routingSessionEnd(t), routingSessionEnd(t), sessionFrame(t, directory, err))
	return &directoryReceiptFrames{scriptFrames: &scriptFrames{input: input}}
}

func TestSharedDirectoryRefreshAcceptanceOnlyActualDirectoryChangesMoveSession(t *testing.T) {
	db, _ := sessionFixture(t)
	for _, tc := range []struct {
		agents  []string
		changed bool
	}{
		{[]string{"second", "first"}, true},
		{[]string{"first", "second"}, false},
		{[]string{"first"}, true},
		{nil, true},
		{nil, false},
	} {
		frames := directoryRoundFrames(t, tc.agents)
		result, _, err := sessionSide(frames, db.PeerMessages()).OpenDial(context.Background())
		sessionOK(t, err)
		sessionEqual(t, result.Moved(), tc.changed)
	}
}

func TestSharedDirectoryRefreshAcceptancePersistedChangeMovesDespiteLostEnd(t *testing.T) {
	db, _ := sessionFixture(t)
	frames := directoryRoundFrames(t, []string{"remote-reviewer"})
	frames.loseReceipt = true
	result, _, err := sessionSide(frames, db.PeerMessages()).OpenDial(context.Background())
	sessionErrorIs(t, err, io.ErrUnexpectedEOF)
	agents, err := db.PeerMessages().PeerDirectory(context.Background(), sessionContact)
	sessionOK(t, err)
	if !reflect.DeepEqual(agents, []string{"remote-reviewer"}) {
		t.Fatalf("directory was not committed before receipt loss: %v", agents)
	}
	sessionEqual(t, result.Moved(), true)
}
