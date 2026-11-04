package chat_test

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func recipientScopeHarness(t *testing.T) (*chatHarness, string) {
	t.Helper()
	h, key := sharePickerHarness(t, "reviewer", "local-only")
	contactRecipientRefresh(t, h, key, "reviewer", "remote-only")
	return h, key
}

func recipientScopeSend(t *testing.T, h *chatHarness) protocol.Message {
	t.Helper()
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "paired-tab send uses contact operation", req.ContactOp, protocol.ContactOpSend)
	return req
}

func recipientScopeNoLocalCopy(t *testing.T, h *chatHarness) {
	t.Helper()
	chatEqual(t, "paired message creates no local transcript copy", len(h.m.State().Entries), 0)
	for _, req := range h.requests {
		if req.Type == protocol.TypeOperatorSend {
			t.Fatal("paired message also sent to local agents")
		}
	}
}

func TestContactRecipientScopeAcceptanceOperatorHintNamesWorkspace(t *testing.T) {
	h, _ := recipientScopeHarness(t)
	for _, want := range []string{"→ Office's operator", "your agents here won't get it"} {
		v3Contains(t, h.m.State().Hint, want)
	}
	v2Type(h, "plain message")
	v3Contains(t, h.m.State().Hint, "→ Office's operator")
	chatEqual(t, "guidance sends nothing", h.m.State().InFlight, 0)
}

func TestContactRecipientScopeAcceptancePlaceholderNamesWorkspace(t *testing.T) {
	h, _ := recipientScopeHarness(t)
	view := ansi.Strip(h.m.View().Content)
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Message ") {
			v3Contains(t, line, "Office")
			if strings.Contains(strings.ToLower(line), "everyone") {
				t.Fatal("paired-tab placeholder implies a local broadcast")
			}
			return
		}
	}
	t.Fatal("empty composer placeholder is not visible")
}

func TestContactRecipientScopeAcceptanceLeadingAgentHintIsRemote(t *testing.T) {
	h, _ := recipientScopeHarness(t)
	v2Type(h, "@reviewer please review")
	for _, want := range []string{"→ reviewer@Office", "your agents here won't get it"} {
		v3Contains(t, h.m.State().Hint, want)
	}
	chatEqual(t, "guidance preserves addressed draft", h.m.State().Draft, "@reviewer please review")
}

func TestContactRecipientScopeAcceptanceSharedNameRoutesOnlyToPeer(t *testing.T) {
	h, key := recipientScopeHarness(t)
	v2Type(h, "@reviewer please review")
	req := recipientScopeSend(t, h)
	chatEqual(t, "name collision uses actual paired workspace", req.ContactKey, key)
	if !slices.Equal(req.ContactTo, []string{"reviewer"}) {
		t.Fatalf("name collision changed remote recipients: %v", req.ContactTo)
	}
	chatEqual(t, "remote address removed from body", req.ContactText, "please review")
	queued := contactMessagesQueued(req)
	queued.ContactMessage.To = req.ContactTo
	h.reply(req, queued)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	recipientScopeNoLocalCopy(t, h)
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "reviewer@Office")
	v3Contains(t, view, "queued")
}

func TestContactRecipientScopeAcceptanceLocalOnlyExplainedBeforeEnter(t *testing.T) {
	h, _ := recipientScopeHarness(t)
	v2Type(h, "@local-only please review")
	for _, want := range []string{"local-only is your agent here", "this tab writes to Office", "Send it in * instead"} {
		v3Contains(t, h.m.State().Hint, want)
	}
	chatEqual(t, "explanation preserves draft", h.m.State().Draft, "@local-only please review")
}

func TestContactRecipientScopeAcceptanceLocalOnlyBlocksWholeDraft(t *testing.T) {
	for _, draft := range []string{"@local-only please review", "@reviewer @local-only please review"} {
		t.Run(draft, func(t *testing.T) {
			h, _ := recipientScopeHarness(t)
			v2Type(h, draft)
			h.input(chatKey(tea.KeyEnter, 0))
			chatEqual(t, "local-only address never sends", h.m.State().InFlight, 0)
			chatEqual(t, "whole draft kept exactly", h.m.State().Draft, draft)
			recipientScopeNoLocalCopy(t, h)
		})
	}
}

func TestContactRecipientScopeAcceptanceUnknownNameStillUsesPeerRefusalPath(t *testing.T) {
	h, key := recipientScopeHarness(t)
	v2Type(h, "@unknown-name question")
	req := recipientScopeSend(t, h)
	chatEqual(t, "unknown name still goes to selected peer", req.ContactKey, key)
	if !slices.Equal(req.ContactTo, []string{"unknown-name"}) {
		t.Fatalf("unknown remote name changed: %v", req.ContactTo)
	}
	h.reply(req, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrContactRefused, Detail: "not_shared"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "remote refusal retains exact draft", h.m.State().Draft, "@unknown-name question")
}

func TestContactRecipientScopeAcceptanceUnreadDirectoryDoesNotBlockLocalName(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	h := contactChatHarness(t, contactChatRow(key, "Office", true))
	revision := h.m.State().Revision + 1
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventPresenceChanged, AgentID: "local-only", SessionID: "local-session", State: "connected", Watermark: protocol.Watermark{Epoch: "epoch", Revision: revision}}}
	h.await(func() bool { return h.m.State().Revision == revision })
	h.input(chatKey(tea.KeyTab, 0))
	page := h.take(protocol.TypeOperatorContact) // Keep the directory response pending.
	chatEqual(t, "directory remains unread", page.ContactOp, protocol.ContactOpList)
	v2Type(h, "@local-only question")
	req := recipientScopeSend(t, h)
	chatEqual(t, "unread directory cannot imply a local-only address", req.ContactKey, key)
	if !slices.Equal(req.ContactTo, []string{"local-only"}) {
		t.Fatalf("unread directory silently changed routing: %v", req.ContactTo)
	}
}

func TestContactRecipientScopeAcceptanceKnownEmptyDirectoryStillBlocksLocalName(t *testing.T) {
	h, key := recipientScopeHarness(t)
	contactRecipientRefresh(t, h, key)
	v2Type(h, "@local-only question")
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "empty known directory is not an unread directory", h.m.State().InFlight, 0)
	chatEqual(t, "known-empty block keeps exact draft", h.m.State().Draft, "@local-only question")
}

func TestContactRecipientScopeAcceptanceDirectoryRefreshUpdatesDraftScope(t *testing.T) {
	h, key := recipientScopeHarness(t)
	v2Type(h, "@local-only question")
	v3Contains(t, h.m.State().Hint, "local-only is your agent here")
	contactRecipientRefresh(t, h, key, "reviewer", "local-only")
	chatEqual(t, "remote sharing update leaves draft alone", h.m.State().Draft, "@local-only question")
	v3Contains(t, h.m.State().Hint, "→ local-only@Office")
	req := recipientScopeSend(t, h)
	if !slices.Equal(req.ContactTo, []string{"local-only"}) {
		t.Fatalf("newly shared peer name did not win: %v", req.ContactTo)
	}
}

func TestContactRecipientScopeAcceptanceMessageStatesStayWithRecipients(t *testing.T) {
	for _, state := range []string{"queued", "delivered", "read", "refused", "expired"} {
		t.Run(state, func(t *testing.T) {
			row := protocol.ContactMessageView{ID: "state-row", Cursor: 1, Direction: "out", State: state, Text: "question", To: []string{"reviewer"}}
			h, _ := contactMessagesHarness(t, row)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			view := ansi.Strip(h.m.View().Content)
			v3Contains(t, view, "reviewer@remote")
			v3Contains(t, view, state)
			recipientScopeNoLocalCopy(t, h)
		})
	}
}
