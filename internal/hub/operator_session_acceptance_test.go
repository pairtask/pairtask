package hub

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func sessionQueueNext(t *testing.T, s *OperatorSession) protocol.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m, ok := s.Next(ctx)
	if !ok {
		t.Fatal("session queue did not produce expected frame")
	}
	return m
}

func sessionQueueEvent(revision uint64) protocol.Message {
	return protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: protocol.EventPresenceChanged, Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}, AgentID: "agent",
	}}
}

func sessionQueueCut(s *OperatorSession, revision uint64) {
	s.cut(protocol.Message{Type: protocol.TypeOperatorSnapshot, RequestID: "snapshot", Snapshot: &protocol.OperatorSnapshot{
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision},
	}})
}

func sessionQueueEmpty(t *testing.T, s *OperatorSession) {
	t.Helper()
	s.Finish()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if msg, ok := s.Next(ctx); ok {
		t.Fatalf("unexpected queued frame: %+v", msg)
	}
	if ctx.Err() != nil {
		t.Fatal("finished queue did not stop promptly")
	}
}

func sessionRequireQueueEvent(t *testing.T, msg protocol.Message, kind string, revision uint64) {
	t.Helper()
	if msg.Event == nil {
		t.Fatalf("missing event: %+v", msg)
	}
	if msg.Event.Kind != kind {
		t.Fatalf("event kind = %s, want %s", msg.Event.Kind, kind)
	}
	if msg.Event.Watermark.Revision != revision {
		t.Fatalf("event revision = %d, want %d", msg.Event.Watermark.Revision, revision)
	}
}

func TestOperatorSessionQueueOverflowRequiresSnapshotAndPreservesResponses(t *testing.T) {
	s := NewOperatorSession(1)
	sessionQueueCut(s, 0)
	sessionQueueNext(t, s)
	s.pushResponse(protocol.Message{Type: protocol.TypeAck, RequestID: "kept"})
	for i := 1; i <= 257; i++ {
		s.pushEvent(sessionQueueEvent(uint64(i)))
	}
	if got := sessionQueueNext(t, s); got.RequestID != "kept" {
		t.Fatalf("overflow dropped response: %+v", got)
	}
	marker := sessionQueueNext(t, s)
	sessionRequireQueueEvent(t, marker, protocol.EventResyncRequired, 257)
	// Once the marker is drained, more changes must stay suppressed until cut.
	for i := 258; i <= 300; i++ {
		s.pushEvent(sessionQueueEvent(uint64(i)))
	}
	sessionQueueCut(s, 300)
	s.pushEvent(sessionQueueEvent(301))
	if got := sessionQueueNext(t, s); got.Type != protocol.TypeOperatorSnapshot {
		t.Fatalf("delta preceded resync response: %+v", got)
	}
	sessionRequireQueueEvent(t, sessionQueueNext(t, s), protocol.EventPresenceChanged, 301)
	sessionQueueEmpty(t, s)
}

func TestOperatorSessionQueueSnapshotSupersedesEventsButKeepsResponses(t *testing.T) {
	s := NewOperatorSession(1)
	s.pushEvent(sessionQueueEvent(1)) // subscription not established
	sessionQueueCut(s, 1)
	if got := sessionQueueNext(t, s); got.Type != protocol.TypeOperatorSnapshot {
		t.Fatalf("event before first snapshot: %+v", got)
	}
	s.pushEvent(sessionQueueEvent(2))
	s.pushResponse(protocol.Message{Type: protocol.TypeAck, RequestID: "receipt"})
	s.pushEvent(sessionQueueEvent(3))
	sessionQueueCut(s, 3)
	s.pushEvent(sessionQueueEvent(4))
	if got := sessionQueueNext(t, s); got.RequestID != "receipt" {
		t.Fatalf("cut lost correlated response: %+v", got)
	}
	if got := sessionQueueNext(t, s); got.Type != protocol.TypeOperatorSnapshot {
		t.Fatalf("cut failed to discard old events: %+v", got)
	}
	if got := sessionQueueNext(t, s); got.Event == nil || got.Event.Watermark.Revision != 4 {
		t.Fatalf("post-cut event order: %+v", got)
	}
	sessionQueueEmpty(t, s)
}

func TestOperatorSessionQueueRequestIDsStayInFlightUntilHandedToWriter(t *testing.T) {
	s := NewOperatorSession(1)
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("request-%d", i)
		if err := s.Begin(id); err != nil {
			t.Fatal(err)
		}
		s.pushResponse(protocol.Message{Type: protocol.TypeAck, RequestID: id})
	}
	if err := s.Begin("request-0"); err == nil {
		t.Fatal("duplicate in-flight ID accepted")
	}
	if err := s.Begin("ninth"); err == nil {
		t.Fatal("ninth in-flight request accepted")
	}
	first := sessionQueueNext(t, s)
	if err := s.Begin(first.RequestID); err == nil {
		t.Fatal("dequeue freed ID before handoff to writer")
	}
	if err := s.Begin("ninth"); err == nil {
		t.Fatal("dequeue freed capacity before handoff to writer")
	}
	s.Handed(first)
	if err := s.Begin(first.RequestID); err != nil {
		t.Fatalf("handed-off ID not reusable: %v", err)
	}
}

func TestOperatorSessionQueueProtocolErrorFitsWhenResponsesAreFull(t *testing.T) {
	s := NewOperatorSession(1)
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("request-%d", i)
		if err := s.Begin(id); err != nil {
			t.Fatal(err)
		}
		s.pushResponse(protocol.Message{Type: protocol.TypeAck, RequestID: id})
	}
	if !s.Control(protocol.Message{Type: protocol.TypeError, RequestID: "ninth", Code: protocol.ErrMalformedFrame}) {
		t.Fatal("response capacity consumed protocol-error reserve")
	}
	s.Finish()
	for i := 0; i < 8; i++ {
		sessionQueueNext(t, s)
	}
	if got := sessionQueueNext(t, s); got.Type != protocol.TypeError || got.RequestID != "ninth" {
		t.Fatalf("protocol error lost: %+v", got)
	}
	sessionQueueEmpty(t, s)
}

func TestOperatorSessionQueueSecondOverflowAfterSnapshotIsNotLost(t *testing.T) {
	s := NewOperatorSession(1)
	for round := 0; round < 2; round++ {
		base := uint64(round * 300)
		sessionQueueCut(s, base)
		sessionQueueNext(t, s)
		for i := 1; i <= 257; i++ {
			s.pushEvent(sessionQueueEvent(base + uint64(i)))
		}
		marker := sessionQueueNext(t, s)
		if marker.Event == nil || marker.Event.Kind != protocol.EventResyncRequired {
			t.Fatalf("round %d missing resync: %+v", round, marker)
		}
		if marker.Event.Watermark.Revision != base+257 {
			t.Fatalf("round %d stale marker: %+v", round, marker)
		}
	}
	sessionQueueEmpty(t, s)
}

func TestOperatorSessionStaleCloseCannotReleaseReplacement(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.Done() }()
	go h.Run(ctx)
	old := NewOperatorSession(1)
	request := protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "open"}
	if got := h.OpenOperatorSession(ctx, request, old); got.Type != protocol.TypeOperatorSession {
		t.Fatalf("old open: %+v", got)
	}
	h.CloseOperatorSession(old)
	replacement := NewOperatorSession(1)
	if got := h.OpenOperatorSession(ctx, request, replacement); got.Type != protocol.TypeOperatorSession {
		t.Fatalf("replacement open: %+v", got)
	}
	h.CloseOperatorSession(old)
	if got := h.OpenOperatorSession(ctx, request, NewOperatorSession(1)); got.Code != protocol.ErrOwnerBusy {
		t.Fatalf("stale release removed new owner: %+v", got)
	}
	h.CloseOperatorSession(replacement)
}
