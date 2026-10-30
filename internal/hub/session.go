package hub

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"

	"github.com/google/uuid"
)

// OperatorSession is the outbound side of one live operator connection. The
// hub appends to it without blocking; the server's single writer drains it in
// order, so a snapshot response always precedes later events.
type OperatorSession struct {
	ID       string
	UID      int
	OpenedAt time.Time

	gen      uint64
	mu       sync.Mutex
	queue    []outbound
	events   int
	controls int
	baseline bool
	resync   bool
	inflight map[string]bool
	closing  bool
	wake     chan struct{}
}

type outboundKind int

const (
	outResponse outboundKind = iota
	outEvent
	outControl
)

type outbound struct {
	msg  protocol.Message
	kind outboundKind
}

var (
	ErrTooManyRequests  = errors.New("too many requests in flight")
	ErrDuplicateRequest = errors.New("request_id is already in flight")
)

// NewOperatorSession prepares an unopened session for an authenticated peer.
func NewOperatorSession(uid int) *OperatorSession {
	return &OperatorSession{ID: uuid.NewString(), UID: uid, OpenedAt: time.Now().UTC(), inflight: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// Begin admits a request; its ID is released when the writer takes its
// response (Handed).
func (s *OperatorSession) Begin(requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[requestID] {
		return ErrDuplicateRequest
	}
	if len(s.inflight) >= protocol.SessionInFlight {
		return ErrTooManyRequests
	}
	s.inflight[requestID] = true
	return nil
}

// Control queues a protocol error or resync marker in a reserved slot. It
// reports false when both slots are taken.
func (s *OperatorSession) Control(msg protocol.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pushControl(msg)
}

func (s *OperatorSession) pushControl(msg protocol.Message) bool {
	if s.controls >= protocol.SessionControlSlots {
		return false
	}
	s.controls++
	s.queue = append(s.queue, outbound{msg, outControl})
	s.signal()
	return true
}

// Finish lets the writer drain what is queued, then stop.
func (s *OperatorSession) Finish() {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	s.signal()
}

// Next blocks until a frame is ready, or returns false once finished and drained.
func (s *OperatorSession) Next(ctx context.Context) (protocol.Message, bool) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			out := s.queue[0]
			s.queue = s.queue[1:]
			s.release(out)
			s.mu.Unlock()
			return out.msg, true
		}
		closing := s.closing
		s.mu.Unlock()
		if closing {
			return protocol.Message{}, false
		}
		select {
		case <-ctx.Done():
			return protocol.Message{}, false
		case <-s.wake:
		}
	}
}

// Handed frees a response's request ID once the single writer has taken the
// response to write, before its bytes go out. A client may reuse the ID as
// soon as it reads that response, and is never refused for it; the writer is
// FIFO, so an answer to a reused ID still comes after the earlier one.
func (s *OperatorSession) Handed(msg protocol.Message) {
	if msg.RequestID == "" {
		return
	}
	s.mu.Lock()
	delete(s.inflight, msg.RequestID)
	s.mu.Unlock()
}

func (s *OperatorSession) release(out outbound) {
	switch out.kind {
	case outEvent:
		s.events--
	case outControl:
		s.controls--
	}
}

func (s *OperatorSession) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *OperatorSession) pushResponse(msg protocol.Message) {
	s.mu.Lock()
	s.queue = append(s.queue, outbound{msg, outResponse})
	s.mu.Unlock()
	s.signal()
}

// pushEvent never blocks the hub. Overflow discards queued events and sends
// one resync marker; deltas stay suppressed until the next snapshot cut.
func (s *OperatorSession) pushEvent(msg protocol.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.baseline || s.resync {
		return
	}
	if s.events >= protocol.SessionEventQueue {
		s.dropEvents()
		s.resync = true
		// The marker bypasses the control slots: at most one is queued per cut.
		marker := protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventResyncRequired, Watermark: msg.Event.Watermark}}
		s.queue = append(s.queue, outbound{marker, outResponse})
		s.signal()
		return
	}
	s.events++
	s.queue = append(s.queue, outbound{msg, outEvent})
	s.signal()
}

// cut installs a snapshot baseline: queued events are superseded and the
// snapshot response is queued before any later event.
func (s *OperatorSession) cut(resp protocol.Message) {
	s.mu.Lock()
	s.dropEvents()
	s.resync = false
	s.baseline = true
	s.queue = append(s.queue, outbound{resp, outResponse})
	s.mu.Unlock()
	s.signal()
}

func (s *OperatorSession) dropEvents() {
	kept := s.queue[:0]
	for _, out := range s.queue {
		if out.kind != outEvent {
			kept = append(kept, out)
		}
	}
	s.queue = kept
	s.events = 0
}

// forceResync tells the owner to stop applying deltas when the hub could not
// describe a change; it clears at the next snapshot cut.
func (s *OperatorSession) forceResync(wm protocol.Watermark) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.baseline || s.resync {
		return
	}
	s.dropEvents()
	s.resync = true
	marker := protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventResyncRequired, Watermark: wm}}
	s.queue = append(s.queue, outbound{marker, outResponse})
	s.signal()
}
