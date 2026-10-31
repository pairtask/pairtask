package chat_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairMenuAcceptanceBareEnterOffersCommandsWithoutRunning(t *testing.T) {
	h := pairingChatHarness(t, true)
	v2Type(h, "keep my draft")
	v2Command(h, "/pair")
	commandCompleteItems(t, h, []string{"open", "status", "cancel", "accept", "reject", "redeem", "settle"})
	chatEqual(t, "menu sends nothing", h.m.State().InFlight, 0)
	chatEqual(t, "draft retained", h.m.State().Draft, "keep my draft")
	v3Contains(t, ansi.Strip(h.m.View().Content), "paired workspaces stay")
	h.input(chatKey(tea.KeyEnter, 0))
	pairingChatRequest(h, "open")
}

func TestPairMenuAcceptanceBareAcceptOffersIdentifiedPendingRequests(t *testing.T) {
	h := pairingChatHarness(t, true)
	key := "sha256:" + strings.Repeat("a", 64)
	at := time.Date(2026, 10, 30, 12, 34, 0, 0, time.Local)
	pairUXRequests(h, []protocol.PairingRequestView{
		{IdentityKey: key, Name: "My laptop", State: "pending_acceptance", FirstPairedAt: at},
		{IdentityKey: "sha256:" + strings.Repeat("b", 64), Name: "Already paired", State: "accepted"},
	}, false)
	v2Command(h, "/pair accept")
	commandCompleteItems(t, h, []string{strings.Repeat("a", 12)})
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"My laptop", strings.Repeat("a", 12), "12:34", "address unknown"} {
		v3Contains(t, view, want)
	}
	chatEqual(t, "opening picker makes no decision", h.m.State().InFlight, 0)
	h.input(chatKey(tea.KeyEnter, 0))
	req := pairingChatRequest(h, "accept")
	chatEqual(t, "selected identity", req.PairingTarget, key)
}

func TestPairMenuAcceptanceCancelNamesInvitationAndNeverRevealsCode(t *testing.T) {
	h := pairingChatHarness(t, true)
	pairingChatOpen(h)
	h.input(chatKey(tea.KeyEscape, 0))
	v2Command(h, "/pair cancel")
	commandCompleteItems(t, h, []string{"invitation-1"})
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "invitation")
	v3Contains(t, view, "expires")
	v3Contains(t, view, "3 attempts left")
	pairingChatNoCode(h)
	chatEqual(t, "opening cancel picker changes nothing", h.m.State().InFlight, 0)
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "Tab makes selected invitation visible", h.m.State().CommandLine.Text, "pair cancel invitation-1 ")
	chatEqual(t, "selecting invitation changes nothing", h.m.State().InFlight, 0)
	h.input(chatKey(tea.KeyEnter, 0))
	req := pairingChatRequest(h, "cancel")
	chatEqual(t, "cancel is scoped to the invitation", req.PairingTarget, "invitation-1")
}

func TestPairMenuAcceptanceAcceptLoadsRequestsWithoutPriorStatus(t *testing.T) {
	h := pairingChatHarness(t, true)
	v2Command(h, "/pair accept")
	req := h.take(protocol.TypeOperatorPairing)
	if req.PairingOp != protocol.PairingOpList && req.PairingOp != protocol.PairingOpStatus {
		t.Fatalf("opening an unloaded picker mutated pairing: %s", req.PairingOp)
	}
	key := "sha256:" + strings.Repeat("c", 64)
	h.reply(req, pairingChatReply(t, "", []string{key}, 1))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	commandCompleteItems(t, h, []string{strings.Repeat("c", 12)})
}

func TestPairMenuAcceptanceCancelWithNoOpenInvitationNeverMutates(t *testing.T) {
	for _, state := range []string{"missing", "expired", "consumed"} {
		t.Run(state, func(t *testing.T) {
			h := pairingChatHarness(t, true)
			v2Command(h, "/pair status")
			req := pairingChatRequest(h, "status")
			snapshot := &protocol.PairingSnapshot{}
			if state != "missing" {
				snapshot.Invitation = &protocol.InvitationView{ID: "old-invitation", Expired: state == "expired", Consumed: state == "consumed"}
			}
			h.reply(req, protocol.Message{Type: protocol.TypeOperatorPairingResult, PairingResult: snapshot})
			h.await(func() bool { return h.m.State().InFlight == 0 })
			h.input(chatKey(tea.KeyEscape, 0))
			v2Command(h, "/pair cancel")
			chatEqual(t, "no invitation to mutate", h.m.State().InFlight, 0)
			v3Contains(t, strings.ToLower(h.m.State().Status), "no open invitation")
		})
	}
}

func TestPairMenuAcceptanceEmptyRequestsExplainNothingToAccept(t *testing.T) {
	h := pairingChatHarness(t, true)
	pairUXRequests(h, nil, false)
	v2Command(h, "/pair accept")
	chatEqual(t, "empty list never decides", h.m.State().InFlight, 0)
	v3Contains(t, strings.ToLower(h.m.State().Status), "no pending")
}

func TestPairMenuAcceptanceLateLoadDoesNotReopenDismissedMenu(t *testing.T) {
	h := pairingChatHarness(t, true)
	v2Command(h, "/pair accept")
	req := h.take(protocol.TypeOperatorPairing)
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyEscape, 0))
	key := "sha256:" + strings.Repeat("d", 64)
	h.reply(req, pairingChatReply(t, "", []string{key}, 1))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "late read cannot reopen command mode", h.m.State().CommandLine == nil, true)
	chatEqual(t, "late read cannot reopen picker", h.m.State().Picker == nil, true)
	chatEqual(t, "late read cannot open status panel", h.m.State().Panel, "")
}
