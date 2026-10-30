package chat_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestRemoteReviewChatAcceptanceQueuedAskIsNotRefused(t *testing.T) {
	h, key := contactMessagesHarness(t)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/pair review @reviewer pairtask/pairtask#235 base head")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "review ask", req.ContactOp, protocol.ContactOpReviewAsk)
	chatEqual(t, "selected workspace", req.ContactKey, key)
	resp := contactMessagesQueued(req)
	resp.ContactMessage.ReviewAsk = req.ReviewAsk
	resp.ContactMessage.Text = "Review request: pairtask/pairtask#235"
	h.reply(req, resp)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	if strings.Contains(view, "refused") {
		t.Fatalf("successful ask reported refused: %s", view)
	}
	v3Contains(t, view, "pairtask/pairtask#235")
	v3Contains(t, view, "queued")
}
