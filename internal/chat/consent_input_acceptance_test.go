package chat_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

const consentFixtureID = "held-immutable-request-293"
const consentFixtureText = "HELD PRIVATE BODY 293"

func consentInputHarness(t *testing.T) *chatHarness {
	t.Helper()
	s := newChatScript()
	h := &chatHarness{t: t, wire: s, results: make(chan tea.Msg, 128), done: make(chan struct{}), dials: make(chan chatDialResult, 8)}
	var session protocol.SessionInfo
	data, _ := json.Marshal(map[string]any{"session_id": "consent-owner", "epoch": "epoch", "gen": 7, "capabilities": append(append([]string{}, protocol.SessionCapabilities...), "consent_prompts")})
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatal(err)
	}
	h.dials <- chatDialResult{wire: s, session: session}
	h.m = chat.New(chat.Options{Dial: func(context.Context) (chat.Transport, protocol.SessionInfo, error) {
		select {
		case d := <-h.dials:
			return d.wire, d.session, d.err
		case <-h.done:
			return nil, protocol.SessionInfo{}, context.Canceled
		}
	}})
	t.Cleanup(func() { close(h.done); s.Close() })
	h.launch(h.m.Init())
	h.input(tea.WindowSizeMsg{Width: 160, Height: 50})
	req := h.take(protocol.TypeOperatorSnapshot)
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1}}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func consentInputPage() protocol.Message {
	var response protocol.Message
	payload := map[string]any{"type": "operator_contact_result", "contact_op": "consent_list", "consent_page": map[string]any{"total": 1, "items": []any{map[string]any{
		"id": consentFixtureID, "cursor": 41, "contact": "authenticated-key-293", "workspace_key": "authenticated-key-293", "label": "Friendly label", "claimed_from": "untrusted-sender", "text": consentFixtureText, "digest": "canonical-digest-293", "revision": 3, "state": "pending",
		"received_at": time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC), "expires_at": time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC),
		"targets": []any{map[string]any{"agent": "alpha", "operations": []string{"message"}}, map[string]any{"agent": "beta", "operations": []string{"message"}}},
	}}}}
	data, _ := json.Marshal(payload)
	if err := json.Unmarshal(data, &response); err != nil {
		panic(err)
	}
	return response
}

func consentInputOpen(t *testing.T, h *chatHarness, pages ...protocol.Message) {
	t.Helper()
	v2Command(h, "/pair consent "+consentFixtureID)
	response := consentInputPage()
	if len(pages) > 0 {
		response = pages[0]
	}
	h.await(func() bool {
		consentInputReplyPages(h, response)
		return h.m.State().InFlight == 0
	})
	frame := ansi.Strip(h.m.View().Content)
	if !strings.Contains(frame, consentFixtureText) {
		t.Fatalf("consent command did not open dedicated immutable preview: %s", frame)
	}
	expected := consentInputPage()
	if len(pages) > 0 {
		expected = pages[0]
	}
	consentAssertPreviewTargets(t, frame, expected)
}

func consentInputNoWrites(t *testing.T, h *chatHarness) {
	t.Helper()
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for {
		select {
		case r := <-h.wire.writes:
			h.intake(r)
		default:
			goto drained
		}
	}
drained:
	for _, r := range h.requests {
		t.Errorf("preview input wrote broker request: type=%s op=%s", r.Type, r.ContactOp)
	}
	if h.m.State().Intent != nil || len(h.m.State().QueuedAcks) != 0 {
		t.Fatal("consent preview created send or receipt intent")
	}
}

func TestConsentInputAcceptanceNoDefaultDecision(t *testing.T) {
	h := consentInputHarness(t)
	consentInputOpen(t, h)
	h.input(chatKey('y', tea.ModCtrl))
	consentInputNoWrites(t, h)
}

func TestConsentInputAcceptanceEnterTabPasteCannotDecide(t *testing.T) {
	for _, key := range []string{"enter", "tab", "paste"} {
		t.Run(key, func(t *testing.T) {
			h := consentInputHarness(t)
			h.input(tea.PasteMsg{Content: "preserved draft"})
			before := h.m.State()
			consentInputOpen(t, h)
			h.input(chatKey('1', 0))
			switch key {
			case "enter":
				h.input(chatKey(tea.KeyEnter, 0))
			case "tab":
				h.input(chatKey(tea.KeyTab, 0))
			case "paste":
				h.input(tea.PasteMsg{Content: "5\n"})
			}
			consentInputNoWrites(t, h)
			h.input(chatKey(tea.KeyEscape, 0))
			after := h.m.State()
			if after.Draft != before.Draft || after.Pasted != before.Pasted {
				t.Fatalf("overlay changed draft or paste provenance: before=%q/%v after=%q/%v", before.Draft, before.Pasted, after.Draft, after.Pasted)
			}
		})
	}
}

func TestConsentInputAcceptanceEachChoiceRequiresExplicitConfirmation(t *testing.T) {
	for i, choice := range []string{"once", "session", "always", "decline", "later"} {
		t.Run(choice, func(t *testing.T) {
			h := consentInputHarness(t)
			consentInputOpen(t, h)
			h.input(chatKey(rune('1'+i), 0))
			consentInputNoWrites(t, h)
			h.input(chatKey('y', tea.ModCtrl))
			req := h.take(protocol.TypeOperatorContact)
			consentAssertConfirmation(t, req, choice)
		})
	}
}

func TestConsentInputAcceptanceGlobalShortcutsAreConsumed(t *testing.T) {
	h := consentInputHarness(t)
	consentInputOpen(t, h)
	for _, key := range []tea.KeyPressMsg{chatKey('k', tea.ModCtrl), chatKey('r', tea.ModCtrl), chatKey('1', tea.ModAlt), chatKey(tea.KeyEnter, 0)} {
		h.input(key)
	}
	consentInputNoWrites(t, h)
	if h.m.State().CommandLine != nil {
		t.Fatal("global command shortcut escaped consent overlay")
	}
	if !strings.Contains(ansi.Strip(h.m.View().Content), consentFixtureText) {
		t.Fatal("global shortcut dismissed consent overlay")
	}
}

func consentAssertConfirmation(t *testing.T, req protocol.Message, choice string) {
	t.Helper()
	if req.ContactKey != "authenticated-key-293" {
		t.Fatalf("confirmation trusted label or claimed sender: %q", req.ContactKey)
	}
	if req.ContactOp != "consent_decide" {
		t.Fatalf("confirmation wrote %q", req.ContactOp)
	}
	data, _ := json.Marshal(req)
	var decoded struct {
		Decision *consentConfirmation `json:"consent_decide"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	valid := consentConfirmationValid(decoded.Decision, choice)
	if !valid {
		t.Fatalf("confirmation lost immutable identity or operator generation: %s", data)
	}
}
func consentInputReplyPages(h *chatHarness, response protocol.Message) {
	for i := 0; i < len(h.requests); i++ {
		r := h.requests[i]
		if r.Type == protocol.TypeOperatorContact && r.ContactOp == "consent_list" {
			h.requests = append(h.requests[:i], h.requests[i+1:]...)
			h.reply(r, response)
			i--
		}
	}
}

func consentAssertPreviewTargets(t *testing.T, frame string, expected protocol.Message) {
	t.Helper()
	values := []string{consentFixtureID, "authenticated-key-293", "untrusted-sender"}
	for _, target := range expected.ConsentPage.Items[0].Targets {
		values = append(values, target.Agent)
		values = append(values, target.Operations...)
	}
	for _, s := range values {
		if !strings.Contains(frame, s) {
			t.Errorf("consent preview omitted %q", s)
		}
	}
}

type consentConfirmation struct {
	ID       string `json:"consent_id"`
	Digest   string
	Revision int64
	Choice   string
	Epoch    string
	Gen      uint64
}

func consentConfirmationValid(d *consentConfirmation, choice string) bool {
	if d == nil {
		return false
	}
	identity := d.ID == consentFixtureID && d.Digest == "canonical-digest-293"
	decision := d.Revision == 3 && d.Choice == choice
	owner := d.Epoch == "epoch" && d.Gen == 7
	return identity && decision && owner
}
