package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func contactKeyCommandHarness(t *testing.T, pending ...string) (*chatHarness, string) {
	t.Helper()
	contact := "sha256:" + strings.Repeat("a", 64)
	h := contactChatHarness(t, contactChatRow(contact, "Peer", true))
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	if req.ContactOp != protocol.ContactOpList || req.ContactKey != contact {
		t.Fatalf("opening paired tab requested %+v", req)
	}
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult,
		ContactPage: &protocol.ContactMessagePage{Contact: contact, PendingPersonKeys: pending,
			OwnIdentity: "sha256:" + strings.Repeat("b", 64)}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h, contact
}

func TestContactKeyCommandsAcceptanceOnlyPendingKeyCanBeChosen(t *testing.T) {
	person := "sha256:" + strings.Repeat("c", 64)
	h, contact := contactKeyCommandHarness(t, person)
	if !strings.Contains(ansi.Strip(h.m.View().Content), person) {
		t.Fatal("pending person's key is invisible in the paired tab")
	}
	v2Command(h, "/pair accept-key")
	req := h.take(protocol.TypeOperatorContact)
	if req.ContactOp != protocol.ContactOpAcceptKey || req.ContactKey != contact {
		t.Fatalf("accept-key targeted the wrong contact: %+v", req)
	}
	if req.ContactText != person || req.MessageID == "" {
		t.Fatalf("accept-key did not bind selected pending key: %+v", req)
	}
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult,
		ContactPage: &protocol.ContactMessagePage{Contact: contact}})
	refresh := h.take(protocol.TypeOperatorContact)
	if refresh.ContactOp != protocol.ContactOpList || refresh.ContactKey != contact {
		t.Fatalf("approval did not refresh selected contact: %+v", refresh)
	}
	h.reply(refresh, protocol.Message{Type: protocol.TypeOperatorContactResult,
		ContactPage: &protocol.ContactMessagePage{Contact: contact}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	if strings.Contains(ansi.Strip(h.m.View().Content), person) {
		t.Fatal("approved person key remained pending in the paired tab")
	}
}

func TestContactKeyCommandsAcceptanceAmbiguousApproval(t *testing.T) {
	first, second := "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64)
	h, _ := contactKeyCommandHarness(t, first, second)
	v2Command(h, "/pair accept-key")
	if h.m.State().InFlight != 0 || !strings.Contains(h.m.State().Status, "several person keys") {
		t.Fatalf("ambiguous key was approved: %+v", h.m.State())
	}
	v2Command(h, "/pair accept-key "+second)
	req := h.take(protocol.TypeOperatorContact)
	if req.ContactOp != protocol.ContactOpAcceptKey || req.ContactText != second {
		t.Fatalf("explicit key selection was lost: %+v", req)
	}
	h.reply(req, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrContactRefused, Detail: "refused"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	if !strings.Contains(ansi.Strip(h.m.View().Content), "several person keys") {
		t.Fatal("refused approval hid pending keys")
	}
}

func TestContactKeyCommandsAcceptanceRequireSealsSwitch(t *testing.T) {
	first, second := "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64)
	h, contact := contactKeyCommandHarness(t, first, second)
	v2Command(h, "/pair require-seals on")
	req := h.take(protocol.TypeOperatorContact)
	if req.ContactOp != protocol.ContactOpRequireSeals || req.ContactKey != contact || req.ContactText != "on" {
		t.Fatalf("require-seals command targeted wrong contact: %+v", req)
	}
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorContactResult,
		ContactPage: &protocol.ContactMessagePage{Contact: contact, PendingPersonKeys: []string{first, second}, RequireSeals: true}})
	refresh := h.take(protocol.TypeOperatorContact)
	h.reply(refresh, protocol.Message{Type: protocol.TypeOperatorContactResult,
		ContactPage: &protocol.ContactMessagePage{Contact: contact, PendingPersonKeys: []string{first, second}, RequireSeals: true}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	if !strings.Contains(h.m.State().Status, "unsigned messages are refused") {
		t.Fatalf("require-seals result did not explain policy: %q", h.m.State().Status)
	}
}
