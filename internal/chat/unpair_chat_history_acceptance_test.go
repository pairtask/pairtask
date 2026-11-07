package chat_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestUnpairChatHistoryAcceptanceTombstoneKeepsDraftAndRefusesMutations(t *testing.T) {
	h, _ := unpairChatHarness(t, unpairChatOptions{supported: true, ended: true, open: true})
	view := strings.ToLower(ansi.Strip(h.m.View().Content))
	v3Contains(t, view, "unpaired by you")
	v3Contains(t, view, "history kept")
	v2Type(h, "kept read-only draft")
	h.input(chatKey(tea.KeyEnter, 0))
	unpairChatNoRequest(t, h)
	chatEqual(t, "read-only draft kept", h.m.State().Draft, "kept read-only draft")
	for _, command := range []string{"/ack all", "/pair share worker", "/pair unshare worker", "/pair pause", "/pair resume", "/pair name Changed"} {
		v2Command(h, command)
		unpairChatNoRequest(t, h)
	}
}
func TestUnpairChatHistoryAcceptanceFreshModelRebuildsUnpairedTab(t *testing.T) {
	h, c := unpairChatHarness(t, unpairChatOptions{supported: true, ended: true})
	views := contactChatViews(h)
	chatEqual(t, "retained contact tab", len(views), 1)
	chatEqual(t, "retained identity", views[0].Contact, c.IdentityKey)
	unpairChatOpen(t, h, c)
	v3Contains(t, strings.ToLower(ansi.Strip(h.m.View().Content)), "unpaired")
	// A second model starts from the same retained broker evidence.
	restarted, retained := unpairChatHarness(t, unpairChatOptions{supported: true, ended: true, open: true})
	chatEqual(t, "fresh model same identity", contactChatViews(restarted)[0].Contact, retained.IdentityKey)
	v3Contains(t, strings.ToLower(ansi.Strip(restarted.m.View().Content)), "history kept")
}
func TestUnpairChatHistoryAcceptanceLocalReadsAndEpochSeparatorsRemainVisible(t *testing.T) {
	h, c := unpairChatHarness(t, unpairChatOptions{supported: true, ended: true, epoch: 2})
	old := historyChatRow(1, 1)
	old.ID = "old-local-read"
	old.Text = "RETAINED OLD BODY"
	old.State = "received"
	old.Direction = "in"
	old.ReadAt = nil
	current := historyChatRow(2, 2)
	current.ID = "unsent-current"
	current.Text = "UNSENT CURRENT BODY"
	current.State = "expired"
	current.Direction = "out"
	current.RefusalReason = "unpaired"
	stamp := time.Now()
	current.ReadAt = &stamp
	h.input(chatKey(tea.KeyTab, 0))
	load := h.take(protocol.TypeOperatorContact)
	h.reply(load, contactMessagesPage(c.IdentityKey, current, old))
	read := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "history read operation", read.ContactOp, protocol.ContactOpRead)
	chatEqual(t, "history read remains scoped", read.ContactEpoch, int64(1))
	contactReadReply(h, read)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := strings.ToLower(ansi.Strip(h.m.View().Content))
	for _, want := range []string{"retained old body", "unsent current body", "not sent"} {
		v3Contains(t, view, want)
	}
	unpairChatEpochLabel(t, view, "1")
	unpairChatEpochLabel(t, view, "2")
}
func unpairChatEpochLabel(t *testing.T, view, epoch string) {
	t.Helper()
	found := strings.Contains(view, "pairing "+epoch) || strings.Contains(view, "epoch "+epoch)
	if !found {
		t.Fatalf("history lacks visible separator for pairing %s: %s", epoch, view)
	}
}
