package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"encoding/json"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
	"strings"
	"testing"
)

func roomChatJSON(t *testing.T, raw string) protocol.Message {
	t.Helper()
	var m protocol.Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func roomChatScope(t *testing.T, m protocol.Message, room string) protocol.Message {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["room"] = room
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return roomChatJSON(t, string(data))
}
func roomChatValue(t *testing.T, m protocol.Message) string {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	value, _ := fields["room"].(string)
	return value
}
func roomChatHarness(t *testing.T) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.reply(h.take(protocol.TypeOperatorSnapshot), roomChatJSON(t, `{"type":"operator_snapshot","snapshot":{"watermark":{"epoch":"epoch","revision":1},"rooms":[{"id":"alpha","members":["author"]},{"id":"beta","members":["other"]}],"rooms_total":2}}`))
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory(publicChatMessage("square-message", 3), roomChatScope(t, publicChatMessage("beta-message", 2), "beta"), roomChatScope(t, publicChatMessage("alpha-message", 1), "alpha")))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}
func roomChatOpen(h *chatHarness, room string) {
	h.t.Helper()
	v2Command(h, "/room "+room)
	v3Contains(h.t, h.m.State().Hint, "#"+room)
}
func TestRoomsChatAcceptanceTabsFilterAndCompose(t *testing.T) {
	h := roomChatHarness(t)
	roomChatOpen(h, "alpha")
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "body alpha-message")
	for _, other := range []string{"body beta-message", "body square-message"} {
		if strings.Contains(view, other) {
			t.Fatalf("room view leaked %s", other)
		}
	}
	v2Type(h, "project discussion")
	sent := v2Send(h)
	chatEqual(t, "broadcast destination", sent.To, "*")
	chatEqual(t, "explicit room", roomChatValue(t, sent), "alpha")
}
func TestRoomsChatAcceptanceDraftsStayInTheirRoom(t *testing.T) {
	h := roomChatHarness(t)
	v2Type(h, "Square draft")
	roomChatOpen(h, "alpha")
	v2Type(h, "alpha draft")
	roomChatOpen(h, "beta")
	v2Type(h, "beta draft")
	roomChatOpen(h, "alpha")
	chatEqual(t, "room draft retained", h.m.State().Draft, "alpha draft")
	v2Command(h, "/chat all")
	chatEqual(t, "Square draft retained", h.m.State().Draft, "Square draft")
}
func TestRoomsChatAcceptanceRoomReplyKeepsAudienceFromAll(t *testing.T) {
	h := roomChatHarness(t)
	v2Command(h, "/reply #1")
	v3Contains(t, h.m.State().Hint, "#alpha")
	v2Type(h, "reply in alpha")
	sent := v2Send(h)
	chatEqual(t, "room reply destination", sent.To, "*")
	chatEqual(t, "room reply scope", roomChatValue(t, sent), "alpha")
	chatEqual(t, "parent retained", sent.InReplyTo, "alpha-message")
}
func TestRoomsChatAcceptanceUnlistedRoomRemainsOpenable(t *testing.T) {
	h := roomChatHarness(t)
	v2Command(h, "/room z-last")
	// Unknown/omitted names are opened through idempotent create.
	req := h.take("operator_room")
	h.reply(req, roomChatJSON(t, `{"type":"operator_room_result","room_list":{"rooms":[{"id":"alpha","members":[]}],"rooms_total":65,"rooms_truncated":true}}`))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v3Contains(t, h.m.State().Hint, "#z-last")
	v2Type(h, "late room")
	sent := v2Send(h)
	chatEqual(t, "omitted room still addressable", roomChatValue(t, sent), "z-last")
}
