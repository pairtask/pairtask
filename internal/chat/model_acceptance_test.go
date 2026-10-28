package chat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

// The harness executes commands separately from Update. Its transport detects
// accidental network writes during Update, and responses may be withheld to
// exercise receipt ordering and uncertain outcomes without sleeping.
type chatScript struct {
	in            chan protocol.Message
	writes        chan protocol.Message
	closed        chan struct{}
	once          sync.Once
	wroteInUpdate atomic.Bool
}

func newChatScript() *chatScript {
	return &chatScript{in: make(chan protocol.Message, 64), writes: make(chan protocol.Message, 64), closed: make(chan struct{})}
}
func (s *chatScript) Request(msg protocol.Message) (string, error) {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if strings.HasSuffix(frame.Function, ".(*Model).Update") {
			s.wroteInUpdate.Store(true)
		}
		if !more {
			break
		}
	}
	s.writes <- msg
	return msg.RequestID, nil
}
func (s *chatScript) Incoming() <-chan protocol.Message { return s.in }
func (s *chatScript) Close() error {
	s.once.Do(func() { close(s.closed); close(s.in) })
	return nil
}

type chatHarness struct {
	t        *testing.T
	m        *chat.Model
	wire     *chatScript
	results  chan tea.Msg
	done     chan struct{}
	requests []protocol.Message
	dials    chan chatDialResult
	quiet    chatQuietContacts
}

// chatQuietContacts serves the quiet pairing read that follows a snapshot
// when the broker lists contacts, with no contacts, so generic tests need
// not. A test that takes operator_pairing itself, like the contact fixture,
// gets it instead: nothing is served inside baseline or such a take.
type chatQuietContacts struct {
	armed    bool        // the next pairing list is the quiet read
	expected bool        // its batch was seen, so it was issued
	wire     *chatScript // the connection it was issued on
	id       string      // its request ID, until served or taken
	hold     int         // > 0 while serving is suspended
}

type chatDialResult struct {
	wire    *chatScript
	session protocol.SessionInfo
	err     error
}

func newChatHarness(t *testing.T, bounds chat.Bounds) *chatHarness {
	t.Helper()
	s := newChatScript()
	h := &chatHarness{t: t, wire: s, results: make(chan tea.Msg, 128), done: make(chan struct{}), dials: make(chan chatDialResult, 8)}
	h.dials <- chatDialResult{wire: s, session: protocol.SessionInfo{SessionID: "owner", Epoch: "epoch", Capabilities: protocol.SessionCapabilities, OpenedAt: time.Now()}}
	h.m = chat.New(chat.Options{Bounds: bounds, Backoff: chat.BackoffPolicy{Base: time.Millisecond, Max: 5 * time.Millisecond, Healthy: time.Minute}, Dial: func(context.Context) (chat.Transport, protocol.SessionInfo, error) {
		select {
		case result := <-h.dials:
			return result.wire, result.session, result.err
		case <-h.done:
			return nil, protocol.SessionInfo{}, context.Canceled
		}
	}})
	t.Cleanup(func() { close(h.done); s.Close() })
	h.launch(h.m.Init())
	return h
}

func (h *chatHarness) launch(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				h.launch(child)
			}
			return
		}
		select {
		case h.results <- msg:
		case <-h.done:
		}
	}()
}

func (h *chatHarness) update(msg tea.Msg) tea.Cmd {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	if h.wire.wroteInUpdate.Load() {
		h.t.Fatal("Transport.Request executed during Update")
	}
	return cmd
}
func (h *chatHarness) input(msg tea.Msg) { h.launch(h.update(msg)) }

func (h *chatHarness) await(want func() bool) {
	h.t.Helper()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	for h.serveQuiet(); !want() || h.quietDue(); h.serveQuiet() {
		select {
		case msg := <-h.results:
			h.input(msg)
		case req := <-h.wire.writes:
			h.intake(req)
		case <-timer.C:
			h.t.Fatalf("chat did not reach expected state; requests=%v state=%+v", h.requests, h.m.State())
		}
	}
}

func (h *chatHarness) take(kind string) protocol.Message {
	h.t.Helper()
	if kind == protocol.TypeOperatorPairing {
		h.quiet.hold++
		defer func() { h.quiet.hold-- }()
	}
	index := -1
	h.await(func() bool {
		for i, req := range h.requests {
			if req.Type == kind {
				index = i
				return true
			}
		}
		return false
	})
	req := h.requests[index]
	h.requests = append(h.requests[:index], h.requests[index+1:]...)
	if req.RequestID == h.quiet.id {
		h.quiet.id = ""
	}
	return req
}

// intake records a request, noting the quiet pairing read and the batch
// that issues it with the baseline's inbox and history reads.
func (h *chatHarness) intake(req protocol.Message) {
	switch {
	case !h.quiet.armed:
	case pairingList(req):
		h.quiet.armed, h.quiet.expected, h.quiet.id = false, false, req.RequestID
	case req.Type == protocol.TypeOperatorReceive || req.Type == protocol.TypeOperatorHistory:
		h.quiet.expected = true
	}
	h.requests = append(h.requests, req)
}

// quietDue reports a quiet read already issued, written and on its way, so
// a generic wait never ends with it unseen in flight. One queued behind the
// in-flight bound, or on a connection since replaced, is not waited for.
func (h *chatHarness) quietDue() bool {
	q := h.quiet
	if !q.armed || !q.expected {
		return false
	}
	if q.hold > 0 || q.wire != h.wire {
		return false
	}
	return h.m.State().Queued == 0
}

func pairingList(req protocol.Message) bool {
	return req.Type == protocol.TypeOperatorPairing && req.PairingOp == protocol.PairingOpList
}

// serveQuiet answers a pending quiet pairing read with no contacts, and
// applies the answer.
func (h *chatHarness) serveQuiet() {
	if h.quiet.id == "" || h.quiet.hold > 0 {
		return
	}
	for i, req := range h.requests {
		if req.RequestID == h.quiet.id {
			h.requests = append(h.requests[:i], h.requests[i+1:]...)
			h.quiet.id = ""
			// Applied before anything else, so it is never left in flight.
			h.launch(h.holdResponse(req, protocol.Message{Type: protocol.TypeOperatorPairingResult,
				PairingResult: &protocol.PairingSnapshot{Contacts: []protocol.PairingContactView{}}}))
			return
		}
	}
}

func (h *chatHarness) reply(req, response protocol.Message) {
	if response.Type == protocol.TypeOperatorSnapshot {
		// The session that asked for this snapshot decides whether a quiet
		// pairing read follows it.
		h.quiet = chatQuietContacts{armed: slices.Contains(h.m.State().Session.Capabilities, protocol.CapabilityPairingContacts),
			wire: h.wire, hold: h.quiet.hold}
	}
	response.RequestID = req.RequestID
	h.wire.in <- response
}

// holdResponse applies the response but deliberately withholds its commands,
// so tests can inspect model admission before any receipt can be written.
func (h *chatHarness) holdResponse(req, response protocol.Message) tea.Cmd {
	h.t.Helper()
	h.reply(req, response)
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	for {
		select {
		case msg := <-h.results:
			if frame, ok := msg.(chat.FrameMsg); ok && frame.Frame.RequestID == req.RequestID {
				return h.update(frame)
			}
			h.input(msg)
		case request := <-h.wire.writes:
			h.intake(request)
		case <-timer.C:
			h.t.Fatal("response was not delivered to the model")
		}
	}
}

func (h *chatHarness) baseline() (protocol.Message, protocol.Message) {
	h.t.Helper()
	h.quiet.hold++
	defer func() { h.quiet.hold-- }()
	req := h.take(protocol.TypeOperatorSnapshot)
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}}})
	return h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
}

func chatIncoming(id string, seq uint64) protocol.Message {
	ts := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	return protocol.Message{Type: protocol.TypeMsg, MessageID: id, Seq: seq, From: "author", To: protocol.OperatorID, Origin: protocol.OriginAgent, Durable: true, AckRequested: true, TS: &ts, Payload: json.RawMessage(fmt.Sprintf(`{"text":%q}`, "body "+id))}
}

func chatInbox(messages ...protocol.Message) protocol.Message {
	var upper uint64
	if len(messages) > 0 {
		upper = messages[len(messages)-1].Seq
	}
	return protocol.Message{Type: protocol.TypeOperatorPage, Page: &protocol.OperatorPage{Messages: messages, UpperSeq: upper, NextAfterSeq: upper}}
}

func chatHistory(messages ...protocol.Message) protocol.Message {
	page := &protocol.HistoryPage{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}}
	for _, msg := range messages {
		page.UpperSeq = max(page.UpperSeq, msg.Seq)
		page.NextBeforeSeq = msg.Seq
		page.Entries = append(page.Entries, protocol.HistoryEntry{Message: msg, Direction: "received", SenderSession: "sender-session", OperatorStage: protocol.StageAccepted})
	}
	return protocol.Message{Type: protocol.TypeOperatorHistory, History: page}
}

func chatKey(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	key := tea.KeyPressMsg{Code: code, Mod: mod}
	if printableChatKey(code) && mod == 0 {
		key.Text = string(code)
	}
	return key
}

func printableChatKey(code rune) bool { return code >= 32 && code < 127 }

func chatEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}
