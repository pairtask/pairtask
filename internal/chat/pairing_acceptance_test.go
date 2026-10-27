package chat_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

const pairingTestCode = "739-281"

func pairingChatHarness(t *testing.T, supported bool) *chatHarness {
	t.Helper()
	s := newChatScript()
	h := &chatHarness{t: t, wire: s, results: make(chan tea.Msg, 128), done: make(chan struct{}), dials: make(chan chatDialResult, 8)}
	caps := slices.Clone(protocol.SessionCapabilities)
	if !supported {
		caps = slices.DeleteFunc(caps, func(cap string) bool { return cap == "pairing" })
	}
	h.dials <- chatDialResult{wire: s, session: protocol.SessionInfo{SessionID: "owner", Epoch: "epoch", Capabilities: caps, OpenedAt: time.Now()}}
	h.m = chat.New(chat.Options{Backoff: chat.BackoffPolicy{Base: time.Millisecond, Max: 5 * time.Millisecond}, Dial: func(context.Context) (chat.Transport, protocol.SessionInfo, error) {
		select {
		case result := <-h.dials:
			return result.wire, result.session, result.err
		case <-h.done:
			return nil, protocol.SessionInfo{}, context.Canceled
		}
	}})
	t.Cleanup(func() { close(h.done); s.Close() })
	h.launch(h.m.Init())
	h.input(tea.WindowSizeMsg{Width: 120, Height: 30})
	inbox, history := h.baseline()
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func pairingChatReply(t *testing.T, code string, keys []string, total int) protocol.Message {
	t.Helper()
	requests := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		requests = append(requests, map[string]any{"identity_key": key, "name": "workspace", "state": "pending_acceptance", "first_paired_at": "2026-10-27T12:00:00Z"})
	}
	result := map[string]any{"requests": requests, "requests_total": total, "requests_truncated": total > len(keys)}
	if code != "" {
		result["invitation"] = map[string]any{"id": "invitation-1", "code": code, "expires_at": "2100-01-01T00:00:00Z", "attempts_left": 3, "in_flight": false, "expired": false, "consumed": false}
	}
	data, err := json.Marshal(map[string]any{"type": "operator_pairing_result", "pairing_result": result})
	if err != nil {
		t.Fatal(err)
	}
	return roomChatJSON(t, string(data))
}
func pairingChatFields(t *testing.T, req protocol.Message) map[string]any {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func pairingChatRequest(h *chatHarness, op string) protocol.Message {
	h.t.Helper()
	req := h.take("operator_pairing")
	chatEqual(h.t, "pairing operation", pairingChatFields(h.t, req)["pairing_op"], any(op))
	return req
}
func pairingChatOpen(h *chatHarness) protocol.Message {
	h.t.Helper()
	v2Command(h, "/pair open")
	req := pairingChatRequest(h, "open")
	h.reply(req, pairingChatReply(h.t, pairingTestCode, nil, 0))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(h.t, ansi.Strip(h.m.View().Content), pairingTestCode)
	return req
}
func pairingChatNoCode(h *chatHarness) {
	h.t.Helper()
	if strings.Contains(ansi.Strip(h.m.View().Content), pairingTestCode) {
		h.t.Fatal("closed or disconnected panel retained pairing code")
	}
	data, err := json.Marshal(h.m.State())
	if err != nil {
		h.t.Fatal(err)
	}
	if strings.Contains(string(data), pairingTestCode) {
		h.t.Fatal("pairing code leaked into public chat state")
	}
}
func pairingChatList(h *chatHarness, keys []string, total int) {
	h.t.Helper()
	v2Command(h, "/pair")
	req := pairingChatRequest(h, "list")
	h.reply(req, pairingChatReply(h.t, "", keys, total))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
}

func TestPairingChatAcceptanceCodeIsEphemeralAndPreservesDraft(t *testing.T) {
	h := pairingChatHarness(t, true)
	v2Type(h, "unsent operator draft")
	pairingChatOpen(h)
	chatEqual(t, "opening adds no transcript entry", len(h.m.State().Entries), 0)
	chatEqual(t, "opening preserves draft", h.m.State().Draft, "unsent operator draft")
	for _, size := range []tea.WindowSizeMsg{{Width: 45, Height: 16}, {Width: 120, Height: 30}} {
		h.input(size)
		v2FrameFits(t, h, size.Width, size.Height)
	}
	h.input(chatKey(tea.KeyEscape, 0))
	pairingChatNoCode(h)
	chatEqual(t, "closing preserves draft", h.m.State().Draft, "unsent operator draft")
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	h.reply(req, pairingChatReply(t, "", nil, 0))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	pairingChatNoCode(h)
	chatEqual(t, "status adds no transcript entry", len(h.m.State().Entries), 0)
}

func TestPairingChatAcceptanceDisconnectClearsCodeAndRejectsOldResponse(t *testing.T) {
	h := pairingChatHarness(t, true)
	open := pairingChatOpen(h)
	h.reconnectTo(newChatScript(), "epoch")
	h.take(protocol.TypeOperatorSnapshot)
	pairingChatNoCode(h)
	stale := pairingChatReply(t, pairingTestCode, nil, 0)
	stale.RequestID = open.RequestID
	h.input(chat.FrameMsg{Gen: 1, Frame: stale})
	pairingChatNoCode(h)
}

func TestPairingChatAcceptanceUniquePrefixResolvesToFullIdentity(t *testing.T) {
	for _, op := range []string{"accept", "reject"} {
		t.Run(op, func(t *testing.T) {
			h := pairingChatHarness(t, true)
			pairingChatList(h, []string{"abcdef-identity-one", "fedcba-identity-two"}, 2)
			v2Command(h, "/pair "+op+" abc")
			req := pairingChatRequest(h, op)
			chatEqual(t, "prefix resolved on client", pairingChatFields(t, req)["pairing_target"], any("abcdef-identity-one"))
			chatEqual(t, "decision is not chat message", len(h.m.State().Entries), 0)
		})
	}
}

func TestPairingChatAcceptanceAmbiguousMissingAndPartialPrefixesDoNotSend(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		keys         []string
		total        int
	}{
		{"ambiguous", "abc", []string{"abc-one", "abc-two"}, 2},
		{"missing", "missing", []string{"abc-one"}, 1},
		{"partial", "abc", []string{"abc-one"}, 65},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := pairingChatHarness(t, true)
			pairingChatList(h, tc.keys, tc.total)
			v2Command(h, "/pair accept "+tc.prefix)
			chatEqual(t, "invalid prefix dispatches no request", h.m.State().InFlight, 0)
			chatEqual(t, "invalid prefix queues no request", h.m.State().Queued, 0)
			chatEqual(t, "invalid prefix creates no chat entry", len(h.m.State().Entries), 0)
		})
	}
}

func TestPairingChatAcceptanceOlderBrokerRefusesCommandsLocally(t *testing.T) {
	for _, command := range []string{"/pair", "/pair open", "/pair status", "/pair cancel", "/pair accept abc", "/pair reject abc"} {
		t.Run(command, func(t *testing.T) {
			h := pairingChatHarness(t, false)
			v2Command(h, command)
			chatEqual(t, "unsupported command sends no request", h.m.State().InFlight, 0)
			chatEqual(t, "unsupported command queues no request", h.m.State().Queued, 0)
			view := strings.ToLower(ansi.Strip(h.m.View().Content))
			v3Contains(t, view, "pairing")
			v3Contains(t, view, "support")
		})
	}
}

func TestPairingChatAcceptanceCancelUsesInvitationID(t *testing.T) {
	h := pairingChatHarness(t, true)
	pairingChatOpen(h)
	h.input(chatKey(tea.KeyEscape, 0))
	v2Command(h, "/pair cancel")
	req := pairingChatRequest(h, "cancel")
	chatEqual(t, "cancel names invitation", pairingChatFields(t, req)["pairing_target"], any("invitation-1"))
	h.reply(req, pairingChatReply(t, "", nil, 0))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	pairingChatNoCode(h)
}

func TestPairingChatAcceptanceEmptyPartialListReportsOmittedRequests(t *testing.T) {
	h := pairingChatHarness(t, true)
	v2Command(h, "/pair")
	req := pairingChatRequest(h, "list")
	h.reply(req, pairingChatReply(t, "", nil, 65))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := strings.ToLower(ansi.Strip(h.m.View().Content))
	v3Contains(t, view, "65")
	v3Contains(t, view, "partial")
	chatEqual(t, "omitted requests are not an empty directory", strings.Contains(view, "no pairing requests"), false)
}

func TestPairingIdentityChatAcceptanceShowsFingerprintOrRecoveryError(t *testing.T) {
	fingerprint := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct{ field, value string }{{"self_identity", fingerprint}, {"identity_error", "identity_missing"}} {
		t.Run(tc.field, func(t *testing.T) {
			h := pairingChatHarness(t, true)
			v2Command(h, "/pair status")
			req := pairingChatRequest(h, "status")
			result := map[string]any{"requests": []any{}, "requests_total": 0, "requests_truncated": false, tc.field: tc.value}
			data, err := json.Marshal(map[string]any{"type": "operator_pairing_result", "pairing_result": result})
			if err != nil {
				t.Fatal(err)
			}
			h.reply(req, roomChatJSON(t, string(data)))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			view := strings.ToLower(ansi.Strip(h.m.View().Content))
			if tc.field == "self_identity" {
				v3Contains(t, view, fingerprint)
			} else {
				v3Contains(t, view, "identity")
				v3Contains(t, view, "missing")
			}
			chatEqual(t, "identity status is not a chat message", len(h.m.State().Entries), 0)
		})
	}
}
