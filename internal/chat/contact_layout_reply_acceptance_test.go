package chat_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactLayoutReplyAcceptanceSameIDParentsRespectAuthorAndDirection(t *testing.T) {
	in, out := contactLayoutRow("same-parent", 1), contactLayoutRow("same-parent", 2)
	in.From, in.Text, in.CreatedAt = "remote-parent", "REMOTE PARENT", contactLayoutAt.Add(-20*time.Minute)
	out.Direction, out.From, out.Text, out.CreatedAt = "out", "local-parent", "LOCAL PARENT", contactLayoutAt.Add(-10*time.Minute)
	cases := []struct{ direction, side, parent string }{
		{"in", protocol.ReplyBySender, in.CreatedAt.Local().Format("15:04") + " remote-parent@Peer"},
		{"in", protocol.ReplyByReceiver, out.CreatedAt.Local().Format("15:04") + " local-parent"},
		{"out", protocol.ReplyBySender, out.CreatedAt.Local().Format("15:04") + " local-parent"},
		{"out", protocol.ReplyByReceiver, in.CreatedAt.Local().Format("15:04") + " remote-parent@Peer"},
	}
	for _, tc := range cases {
		t.Run(tc.direction+"-"+tc.side, func(t *testing.T) {
			reply := contactLayoutRow("reply", 3)
			reply.Direction, reply.ReplySide, reply.ReplyID = tc.direction, tc.side, in.ID
			h := contactLayoutHarness(t, in, out, reply)
			view := ansi.Strip(h.m.View().Content)
			want := "↳ re " + tc.parent
			at := contactLayoutLine(t, view, want)
			lines := strings.Split(view, "\n")
			chatEqual(t, "reply preview is its own line", strings.TrimSpace(lines[at]), want)
			chatEqual(t, "reply preview uses body indent", strings.Index(lines[at], want), 2)
			chatEqual(t, "reply body follows preview", strings.TrimSpace(lines[at+1]), reply.Text)
		})
	}
}

func TestContactLayoutReplyAcceptanceMissingOrWrongSideParentUsesFallback(t *testing.T) {
	for _, side := range []string{protocol.ReplyBySender, protocol.ReplyByReceiver, ""} {
		t.Run(side, func(t *testing.T) {
			reply := contactLayoutRow("reply", 2)
			reply.ReplyID, reply.ReplySide = "unavailable-parent-id", side
			h := contactLayoutHarness(t, reply)
			view := ansi.Strip(h.m.View().Content)
			v3Contains(t, view, "↳ re an earlier message")
			if strings.Contains(view, reply.ReplyID) {
				t.Fatal("missing parent became a new displayed reference")
			}
		})
	}
}

func TestContactLayoutReplyAcceptanceWrongDirectionDoesNotUseExistingID(t *testing.T) {
	parent, reply := contactLayoutRow("parent", 1), contactLayoutRow("reply", 2)
	parent.From = "remote-parent"
	reply.ReplyID, reply.ReplySide = parent.ID, protocol.ReplyByReceiver
	h := contactLayoutHarness(t, parent, reply)
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "↳ re an earlier message")
	if strings.Contains(view, "↳ re "+contactLayoutAt.Local().Format("15:04")) {
		t.Fatal("opposite-direction parent used for reply preview")
	}
}

func TestContactLayoutReplyAcceptanceUnknownAuthorNeverGuessesParent(t *testing.T) {
	in, out := contactLayoutRow("same-parent", 1), contactLayoutRow("same-parent", 2)
	in.From, out.From, out.Direction = "remote-parent", "local-parent", "out"
	for _, side := range []string{"", "unknown-author"} {
		t.Run(side, func(t *testing.T) {
			reply := contactLayoutRow("reply", 3)
			reply.ReplyID, reply.ReplySide = in.ID, side
			h := contactLayoutHarness(t, in, out, reply)
			view := ansi.Strip(h.m.View().Content)
			v3Contains(t, view, "↳ re an earlier message")
			if strings.Contains(view, "↳ re "+contactLayoutAt.Local().Format("15:04")) {
				t.Fatal("unknown author guessed an existing parent")
			}
		})
	}
}

func TestContactLayoutReplyAcceptanceDoesNotCrossContactBoundary(t *testing.T) {
	first := contactChatRow("sha256:"+strings.Repeat("a", 64), "First", true)
	second := contactChatRow("sha256:"+strings.Repeat("b", 64), "Second", true)
	h := contactChatHarness(t, first, second)
	h.input(tea.WindowSizeMsg{Width: 200, Height: 40})
	h.input(chatKey(tea.KeyTab, 0))
	load := h.take(protocol.TypeOperatorContact)
	parent := contactLayoutRow("shared-parent-id", 1)
	parent.From = "other-workspace-parent"
	h.reply(load, contactMessagesPage(load.ContactKey, parent))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyTab, 0))
	other := h.take(protocol.TypeOperatorContact)
	if other.ContactKey == load.ContactKey {
		t.Fatal("fixture did not switch workspace")
	}
	reply := contactLayoutRow("reply", 1)
	reply.ReplyID, reply.ReplySide = parent.ID, protocol.ReplyBySender
	h.reply(other, contactMessagesPage(other.ContactKey, reply))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "↳ re an earlier message")
	if strings.Contains(view, parent.From) {
		t.Fatal("parent from another workspace leaked into preview")
	}
}

func TestContactLayoutReplyAcceptanceNarrowPreviewWrapsWithoutLoss(t *testing.T) {
	parent, reply := contactLayoutRow("parent", 1), contactLayoutRow("reply", 2)
	parent.From = "remote-parent-with-long-name"
	reply.ReplyID, reply.ReplySide = parent.ID, protocol.ReplyBySender
	h := contactLayoutHarness(t, parent, reply)
	h.input(tea.WindowSizeMsg{Width: 40, Height: 60})
	v2FrameFits(t, h, 40, 60)
	flat := strings.Join(strings.Fields(ansi.Strip(h.m.View().Content)), "")
	v3Contains(t, flat, "↳re"+contactLayoutAt.Local().Format("15:04")+parent.From+"@Peer")
}
