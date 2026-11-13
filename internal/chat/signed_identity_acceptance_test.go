package chat_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func signedChatHarness(t *testing.T, calls *atomic.Int32) *chatHarness {
	t.Helper()
	s := newChatScript()
	h := &chatHarness{t: t, wire: s, results: make(chan tea.Msg, 128), done: make(chan struct{}), dials: make(chan chatDialResult, 8)}
	h.dials <- chatDialResult{wire: s, session: protocol.SessionInfo{SessionID: "owner", Epoch: "epoch", Capabilities: protocol.SessionCapabilities, OpenedAt: time.Now()}}
	h.m = chat.New(chat.Options{
		Bounds: chat.Bounds{}, Backoff: chat.BackoffPolicy{Base: time.Millisecond, Max: 5 * time.Millisecond, Healthy: time.Minute},
		Dial: func(context.Context) (chat.Transport, protocol.SessionInfo, error) {
			select {
			case result := <-h.dials:
				return result.wire, result.session, result.err
			case <-h.done:
				return nil, protocol.SessionInfo{}, context.Canceled
			}
		},
		Seal: func(m protocol.Message) string {
			calls.Add(1)
			return fmt.Sprintf("sealed:%s:%s", m.MessageID, m.Payload)
		},
	})
	t.Cleanup(func() { close(h.done); s.Close() })
	h.launch(h.m.Init())
	return h
}

func TestChatSignedIdentityAcceptanceRetryKeepsOriginalSeal(t *testing.T) {
	var calls atomic.Int32
	h := signedChatHarness(t, &calls)
	h.ready()
	h.draft("z,a", "original")
	h.input(chatKey(tea.KeyEnter, 0))
	original := h.take(protocol.TypeOperatorSend)
	if original.Seal == "" || calls.Load() != 1 {
		t.Fatalf("chat did not seal once before dispatch: seal=%q calls=%d", original.Seal, calls.Load())
	}
	next := newChatScript()
	h.reconnectTo(next, "epoch")
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}})
	retry := h.take(protocol.TypeOperatorSend)
	if retry.Seal != original.Seal || retry.MessageID != original.MessageID || calls.Load() != 1 {
		t.Fatalf("retry changed signed intent: first=%+v retry=%+v calls=%d", original, retry, calls.Load())
	}
}
