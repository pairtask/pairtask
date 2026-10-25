package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPublicBroadcastAcceptanceReceivedHeaderNamesAudience(t *testing.T) {
	for _, source := range []string{"inbox", "history", "history-and-inbox"} {
		for _, audience := range []struct{ name, to, want, absent string }{
			{"public", protocol.Broadcast, "author → everyone [agent]", "author → you"},
			{"direct", protocol.OperatorID, "author → you [agent]", "author → everyone"},
		} {
			t.Run(source+"/"+audience.name, func(t *testing.T) {
				h := headerAudienceHarness(t, source, audience.to)
				chatEqual(t, "one entry after delivery reconciliation", len(h.m.State().Entries), 1)
				for _, width := range []int{80, 32} {
					h.input(tea.WindowSizeMsg{Width: width, Height: 25})
					plain := strings.Join(strings.Fields(ansi.Strip(h.m.View().Content)), " ")
					if !strings.Contains(plain, audience.want) || strings.Contains(plain, audience.absent) {
						t.Fatalf("width %d: want header %q and not %q in %q", width, audience.want, audience.absent, plain)
					}
				}
			})
		}
	}
}

func headerAudienceHarness(t *testing.T, source, to string) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	inbox, history := h.baseline()
	msg := chatIncoming("audience", 1)
	msg.To = to
	if source == "inbox" {
		h.reply(history, chatHistory())
	} else {
		h.reply(history, chatHistory(msg))
		h.await(func() bool { return len(h.m.State().Entries) == 1 })
	}
	if source == "history" {
		h.reply(inbox, chatInbox())
	} else {
		h.reply(inbox, chatInbox(msg))
		receipt := h.take(protocol.TypeOperatorReceived)
		h.reply(receipt, chatReceipt(msg.MessageID))
	}
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}
