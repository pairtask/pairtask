package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestUnpairChatConfirmationAcceptanceTypedRenderedCardBindsExactSnapshot(t *testing.T) {
	h, c := unpairChatHarness(t, unpairChatOptions{supported: true, open: true})
	v2Type(h, "draft that must not confirm")
	v2Command(h, "/pair unpair")
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"Unpair Office", "aaaaaaaa", "history", "fresh code"} {
		v3Contains(t, strings.ToLower(view), strings.ToLower(want))
	}
	chatEqual(t, "confirmation clears composer", h.m.State().Draft, "")
	v2Command(h, unpairChatPhrase)
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "unpair operation", req.ContactOp, "unpair")
	chatEqual(t, "selected full key", req.ContactKey, c.IdentityKey)
	fields := unpairChatFields(t, req)
	chatEqual(t, "full confirmation", fields["contact_confirm"], any(c.IdentityKey))
	chatEqual(t, "displayed epoch", req.ContactEpoch, int64(7))
	chatEqual(t, "durable operation ID", req.MessageID != "", true)
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "card dispatches at most once", h.m.State().InFlight, 1)
}
func TestUnpairChatConfirmationAcceptanceUnrenderedCardCannotConfirm(t *testing.T) {
	h, _ := unpairChatHarness(t, unpairChatOptions{supported: true, open: true})
	v2Command(h, "/pair unpair")
	v2Command(h, unpairChatPhrase)
	unpairChatNoRequest(t, h)
}
func TestUnpairChatConfirmationAcceptanceDiscardGuardsNeverMutate(t *testing.T) {
	for _, guard := range []string{"empty-enter", "escape", "tab", "paste"} {
		t.Run(guard, func(t *testing.T) {
			h, _ := unpairChatHarness(t, unpairChatOptions{supported: true, open: true})
			v2Command(h, "/pair unpair")
			h.m.View()
			switch guard {
			case "empty-enter":
				h.input(chatKey(tea.KeyEnter, 0))
			case "escape":
				h.input(chatKey(tea.KeyEscape, 0))
			case "tab":
				h.input(chatKey(tea.KeyTab, tea.ModShift))
			case "paste":
				h.input(tea.PasteMsg{Content: unpairChatPhrase})
			}
			v2Command(h, unpairChatPhrase)
			unpairChatNoRequest(t, h)
		})
	}
}
func TestUnpairChatConfirmationAcceptanceWrongTokenOrOldBrokerNeverMutates(t *testing.T) {
	for _, supported := range []bool{false, true} {
		h, _ := unpairChatHarness(t, unpairChatOptions{supported: supported, open: true})
		v2Command(h, "/pair unpair")
		h.m.View()
		phrase := unpairChatPhrase
		if supported {
			phrase = "/pair unpair confirm bbbbbbbb"
		}
		v2Command(h, phrase)
		unpairChatNoRequest(t, h)
	}
}
