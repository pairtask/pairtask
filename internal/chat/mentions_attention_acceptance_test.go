package chat_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func attentionHarness(t *testing.T, supported, partial bool, members ...string) *chatHarness {
	t.Helper()
	if len(members) == 0 {
		members = []string{"alice", "bob", "offline"}
	}
	s := newChatScript()
	h := &chatHarness{t: t, wire: s, results: make(chan tea.Msg, 128), done: make(chan struct{}), dials: make(chan chatDialResult, 8)}
	caps := slices.Clone(protocol.SessionCapabilities)
	if !supported {
		caps = slices.DeleteFunc(caps, func(cap string) bool { return cap == "mentions" })
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
	h.reply(h.take(protocol.TypeOperatorSnapshot), protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{
		Watermark: protocol.Watermark{Epoch: "epoch", Revision: 1},
		Agents:    []protocol.AgentPresence{{AgentID: "alice"}, {AgentID: "bob"}, {AgentID: "mallory"}}, AgentsTotal: 3,
		Rooms: []protocol.RoomInfo{{ID: "alpha", Members: members, MembersTotal: len(members), MembersTruncated: partial}}, RoomsTotal: 1,
	}})
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	msg := publicChatMessage("addressed-history", 1)
	msg.Room, msg.From, msg.Mentions = "alpha", "alice", []string{"bob"}
	h.reply(history, chatHistory(msg))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func TestAttentionMentionsAcceptanceRoomAndGroupKeepWholeAudience(t *testing.T) {
	for _, group := range []bool{false, true} {
		h := attentionHarness(t, true, false)
		if group {
			v2Command(h, "/chat @alice @bob")
		} else {
			roomChatOpen(h, "alpha")
		}
		v2Type(h, "@alice @alice please review")
		v3Contains(t, h.m.State().Hint, "addressed")
		v3Contains(t, h.m.State().Hint, "@alice")
		sent := v2Send(h)
		if !slices.Equal(sent.Mentions, []string{"alice"}) {
			t.Fatalf("wrong attention: %+v", sent)
		}
		chatEqual(t, "typed prefix preserved", v2Body(t, sent), "@alice @alice please review")
		if group {
			if !slices.Equal(sent.Group, []string{"alice", "bob"}) {
				t.Fatalf("group narrowed: %+v", sent)
			}
		} else {
			chatEqual(t, "room broadcast unchanged", sent.To, "*")
			chatEqual(t, "room retained", sent.Room, "alpha")
		}
	}
}

func TestAttentionMentionsAcceptancePickerOnlyOffersMembersAndSkipsTyped(t *testing.T) {
	for _, group := range []bool{false, true} {
		h := attentionHarness(t, true, false)
		if group {
			v2Command(h, "/chat @alice @bob")
		} else {
			roomChatOpen(h, "alpha")
		}
		v2Type(h, "@alice @")
		p := h.m.State().Picker
		if p == nil {
			t.Fatal("member mention picker missing")
		}
		want := []string{"bob", "offline"}
		if group {
			want = []string{"bob"}
		}
		if !slices.Equal(p.Items, want) {
			t.Fatalf("offered %q, want eligible %q", p.Items, want)
		}
	}
}

func TestAttentionMentionsAcceptancePickerOnlyOffersInlineNames(t *testing.T) {
	for _, group := range []bool{false, true} {
		h := attentionHarness(t, true, false, "alice", "all", "worker with spaces", "a@b", "bob")
		if group {
			v2Command(h, `/chat ["alice","all","worker with spaces","a@b","bob"]`)
		} else {
			roomChatOpen(h, "alpha")
		}
		v2Type(h, "@")
		p := h.m.State().Picker
		if p == nil || !slices.Equal(p.Items, []string{"alice", "bob"}) {
			t.Fatalf("picker offers names the inline parser cannot address: %+v", p)
		}
	}
}

func TestAttentionMentionsAcceptanceNonmembersAndAllBlockBeforeSend(t *testing.T) {
	for _, draft := range []string{"@mallory please review", "@all please review", "@alice @mallory review"} {
		h := attentionHarness(t, true, false)
		roomChatOpen(h, "alpha")
		v2Type(h, draft)
		h.input(chatKey(tea.KeyEnter, 0))
		chatEqual(t, "invalid attention has no intent", h.m.State().Intent, nil)
		chatEqual(t, "invalid attention sends nothing", h.m.State().InFlight, 0)
		if h.m.State().Audience.Error == "" {
			t.Fatal("invalid attention not explained")
		}
	}
}

func TestAttentionMentionsAcceptancePartialMembershipDefersToBroker(t *testing.T) {
	h := attentionHarness(t, true, true)
	roomChatOpen(h, "alpha")
	v2Type(h, "@not-listed please review")
	sent := v2Send(h)
	if !slices.Equal(sent.Mentions, []string{"not-listed"}) {
		t.Fatalf("partial snapshot discarded mention: %+v", sent)
	}
	chatEqual(t, "partial room still broadcasts", sent.To, "*")
}

func TestAttentionMentionsAcceptanceOlderBrokerOnlyBlocksMentionedSend(t *testing.T) {
	h := attentionHarness(t, false, false)
	roomChatOpen(h, "alpha")
	v2Type(h, "@alice review")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "unsupported metadata never silently lost", h.m.State().Intent, nil)
	v3Contains(t, h.m.State().Hint+h.m.State().Status, "upgrade")
	h.input(chatKey('u', tea.ModCtrl))
	// Replace the draft through the model's public editor shortcut.
	for remaining := len([]rune(h.m.State().Draft)) + 1; h.m.State().Draft != "" && remaining > 0; remaining-- {
		h.input(chatKey(tea.KeyBackspace, 0))
	}
	v2Type(h, "ordinary room message")
	sent := v2Send(h)
	chatEqual(t, "legacy room send still works", sent.Room, "alpha")
	chatEqual(t, "legacy send has no attention", len(sent.Mentions), 0)
}

func TestAttentionMentionsAcceptanceLiteralPasteEscapeAndBodyMentionsStayLiteral(t *testing.T) {
	for _, tc := range []struct {
		text, body string
		paste      bool
	}{
		{"@alice review", "@alice review", true},
		{`\@alice review`, "@alice review", false},
		{"please ask @alice", "please ask @alice", false},
	} {
		h := attentionHarness(t, true, false)
		roomChatOpen(h, "alpha")
		if tc.paste {
			h.input(tea.PasteMsg{Content: tc.text})
		} else {
			v2Type(h, tc.text)
		}
		sent := v2Send(h)
		chatEqual(t, "literal content never creates attention", len(sent.Mentions), 0)
		chatEqual(t, "expected literal body", v2Body(t, sent), tc.body)
	}
}

func TestAttentionMentionsAcceptanceAllAndDirectRetainExistingRouting(t *testing.T) {
	for _, direct := range []bool{false, true} {
		h := attentionHarness(t, true, false)
		if direct {
			conversationOpen(h, "alice")
		}
		v2Type(h, "@alice review")
		sent := v2Send(h)
		chatEqual(t, "existing direct routing", sent.To, "alice")
		chatEqual(t, "no room/group attention on direct", len(sent.Mentions), 0)
	}
}

func TestAttentionMentionsAcceptanceHeaderShowsStoredAttention(t *testing.T) {
	h := attentionHarness(t, true, false)
	roomChatOpen(h, "alpha")
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "@bob")
	v3Contains(t, view, "body addressed-history")
}

func TestAttentionMentionsAcceptanceRetryKeepsFrozenMetadata(t *testing.T) {
	h := attentionHarness(t, true, false)
	roomChatOpen(h, "alpha")
	v2Type(h, "@alice please review")
	original := v2Send(h)
	next := newChatScript()
	h.reconnectTo(next, "epoch")
	snapshot := h.take(protocol.TypeOperatorSnapshot)
	h.reply(snapshot, protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &protocol.OperatorSnapshot{Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}})
	retry := h.take(protocol.TypeOperatorSend)
	original.RequestID, retry.RequestID = "", ""
	if !reflect.DeepEqual(original, retry) {
		a, _ := json.Marshal(original)
		b, _ := json.Marshal(retry)
		t.Fatalf("frozen mention retry changed: %s -> %s", a, b)
	}
}
