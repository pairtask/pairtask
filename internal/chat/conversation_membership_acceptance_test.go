package chat_test

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestChatConversationMembershipKeepsOnlyDirectTrafficInAgentViews(t *testing.T) {
	h := conversationHarness(t, "alice", "bob", "carol")
	h.input(tea.WindowSizeMsg{Width: 120, Height: 45})
	incomingAlice := chatIncoming("from-alice", 15)
	incomingAlice.From = "alice"
	incomingBob := chatIncoming("from-bob", 14)
	incomingBob.From = "bob"
	direct := chatIncoming("direct-alice", 13)
	direct.From, direct.To, direct.Origin = protocol.OperatorID, "alice", protocol.OriginOperator
	group := chatIncoming("group-alice-bob", 12)
	group.From, group.To, group.Origin = protocol.OperatorID, "", protocol.OriginOperator
	group.Group = []string{"alice", "bob"}
	broadcast := chatIncoming("broadcast-accepted-alice", 11)
	broadcast.From, broadcast.To, broadcast.Origin = protocol.OperatorID, protocol.Broadcast, protocol.OriginOperator
	publicReply := chatIncoming("public-from-alice", 10)
	publicReply.From, publicReply.To = "alice", protocol.Broadcast
	page := chatHistory(incomingAlice, incomingBob, direct, group, broadcast, publicReply)
	for i := 2; i < 5; i++ {
		page.History.Entries[i].Direction = "sent"
	}
	page.History.Entries[4].Recipients = []protocol.RecipientStage{{AgentID: "alice", Stage: protocol.StageAccepted}}
	page.History.Watermark.Revision = 2
	h.event(protocol.EventConversationChanged, "epoch", 2)
	h.reply(h.take(protocol.TypeOperatorHistory), page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	for _, tc := range []struct {
		id                 string
		included, excluded []string
	}{
		{"alice", []string{"from-alice", "direct-alice"}, []string{"from-bob", "group-alice-bob", "broadcast-accepted-alice", "public-from-alice"}},
		{"bob", []string{"from-bob"}, []string{"from-alice", "direct-alice", "group-alice-bob", "broadcast-accepted-alice", "public-from-alice"}},
		{"carol", nil, []string{"from-alice", "from-bob", "direct-alice", "group-alice-bob", "broadcast-accepted-alice", "public-from-alice"}},
	} {
		conversationOpen(h, tc.id)
		frame := ansi.Strip(h.m.View().Content)
		for _, id := range tc.included {
			v3Contains(t, frame, "body "+id)
		}
		for _, id := range tc.excluded {
			if strings.Contains(frame, "body "+id) {
				t.Errorf("%s sees unrelated history %s", tc.id, id)
			}
		}
	}
	v2Command(h, "/chat all")
	frame := ansi.Strip(h.m.View().Content)
	for _, id := range []string{"from-alice", "from-bob", "direct-alice", "group-alice-bob", "broadcast-accepted-alice", "public-from-alice"} {
		v3Contains(t, frame, "body "+id)
	}
}

func TestChatConversationClearCutoffDoesNotHideOtherViews(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	conversationOpen(h, "alice")
	v3Contains(t, ansi.Strip(h.m.View().Content), "body conversation-target")
	v2Command(h, "/clear")
	if strings.Contains(ansi.Strip(h.m.View().Content), "body conversation-target") {
		t.Fatal("clear left target visible")
	}
	v2Command(h, "/chat all")
	v3Contains(t, ansi.Strip(h.m.View().Content), "body conversation-target")
	conversationOpen(h, "alice")
	if strings.Contains(ansi.Strip(h.m.View().Content), "body conversation-target") {
		t.Fatal("view switch lost the clear cutoff")
	}
	chatEqual(t, "clear retains underlying history", len(h.m.State().Entries), 1)
}

func TestChatConversationAllKeepsOneOfThe64SavedSlots(t *testing.T) {
	var ids []string
	for i := 0; i < 64; i++ {
		ids = append(ids, fmt.Sprintf("agent-%02d", i))
	}
	h := conversationHarness(t, ids...)
	for _, id := range ids[:63] {
		conversationOpen(h, id)
		v2Type(h, "protected "+id)
	}
	v2Command(h, "/chat all") // All remains reserved even with an empty draft.
	v2Command(h, "/chat "+ids[63])
	v3Contains(t, h.m.State().Status, "conversation state limit")
	chatEqual(t, "refused view switch leaves All draft", h.m.State().Draft, "")
	h.input(chatKey(tea.KeyEscape, 0))
	for _, id := range []string{ids[0], ids[31], ids[62]} {
		conversationOpen(h, id)
		chatEqual(t, "no protected draft evicted", h.m.State().Draft, "protected "+id)
	}
}

func TestChatConversationNewEpochResetsSelectedAndSavedCutoffs(t *testing.T) {
	h := conversationHarness(t, "alice", "bob")
	conversationOpen(h, "alice")
	v2Command(h, "/clear")
	chatEqual(t, "direct cutoff set", h.m.State().Cutoff, uint64(7))
	v2Command(h, "/chat all")
	v2Command(h, "/clear")
	chatEqual(t, "common cutoff set", h.m.State().Cutoff, uint64(7))
	h.event(protocol.EventResyncRequired, "next-epoch", 1)
	h.take(protocol.TypeOperatorSnapshot)
	chatEqual(t, "selected cutoff reset", h.m.State().Cutoff, uint64(0))
	conversationOpen(h, "alice")
	chatEqual(t, "saved cutoff reset", h.m.State().Cutoff, uint64(0))
}
