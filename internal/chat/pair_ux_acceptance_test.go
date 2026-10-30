package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
	"strings"
	"testing"
)

func pairUXRequests(h *chatHarness, requests []protocol.PairingRequestView, partial bool) {
	h.t.Helper()
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorPairingResult, PairingResult: &protocol.PairingSnapshot{Requests: requests, RequestsTotal: len(requests), RequestsTruncated: partial}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	h.input(chatKey(tea.KeyEscape, 0))
}

func TestPairUXAcceptanceVerbsAndPendingRequestRows(t *testing.T) {
	h := pairingChatHarness(t, true)
	first := "sha256:" + strings.Repeat("a", 64)
	second := "sha256:" + strings.Repeat("b", 64)
	pairUXRequests(h, []protocol.PairingRequestView{
		{IdentityKey: first, Name: "Home Mac", State: "pending_acceptance"},
		{IdentityKey: second, State: "pending_acceptance"},
		{IdentityKey: "sha256:" + strings.Repeat("c", 64), Name: "already accepted", State: "accepted"},
	}, false)
	commandCompleteLine(h, "pair ")
	commandCompleteItems(t, h, []string{"open", "status", "cancel", "accept", "reject", "redeem", "settle"})
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey(tea.KeyEscape, 0))
	commandCompleteLine(h, "pair accept ")
	commandCompleteItems(t, h, []string{strings.Repeat("a", 12), strings.Repeat("b", 12)})
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "Home Mac")
	v3Contains(t, view, "no name given")
	chatEqual(t, "suggestions do not send", h.m.State().InFlight, 0)
	h.input(chatKey(tea.KeyDown, 0))
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "selected key is visible before decision", h.m.State().CommandLine.Text, "pair accept "+strings.Repeat("b", 12)+" ")
	chatEqual(t, "selection only completes", h.m.State().InFlight, 0)
	h.input(chatKey(tea.KeyEnter, 0))
	req := pairingChatRequest(h, "accept")
	chatEqual(t, "wire uses full selected identity", req.PairingTarget, second)
}

func TestPairUXAcceptanceNarrowHintsNeverEchoRedeemArguments(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"pair ac", "/pair accept KEY"}, {"pair reject ", "/pair reject KEY"},
		{"pair settle ", "/pair settle [OPERATION_ID]"},
		{"pair redeem 100.64.0.2:7445 739281 MyMac", "NAME is how this workspace shows on theirs"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			h := pairingChatHarness(t, true)
			commandCompleteLine(h, tc.line)
			hint := h.m.State().Hint
			v3Contains(t, hint, tc.want)
			for _, secret := range []string{"739281", "100.64.0.2", "MyMac"} {
				if strings.Contains(hint, secret) {
					t.Fatalf("hint echoed argument: %q", hint)
				}
			}
			if strings.Contains(tc.line, "redeem ") {
				chatEqual(t, "redeem has no argument suggestions", h.m.State().Picker == nil, true)
			}
			chatEqual(t, "hint has no side effects", h.m.State().InFlight, 0)
		})
	}
}

func TestPairUXAcceptanceSchemePrefixCompletesAndDraftSurvives(t *testing.T) {
	h := pairingChatHarness(t, true)
	key := "sha256:" + strings.Repeat("a", 64)
	pairingChatList(h, []string{key}, 1)
	v2Type(h, "unsent draft")
	commandCompleteLine(h, "pair reject sha256:aaa")
	commandCompleteItems(t, h, []string{strings.Repeat("a", 12)})
	h.input(chatKey(tea.KeyTab, 0))
	chatEqual(t, "scheme removed from displayed choice", h.m.State().CommandLine.Text, "pair reject "+strings.Repeat("a", 12)+" ")
	chatEqual(t, "draft preserved", h.m.State().Draft, "unsent draft")
	h.input(chatKey(tea.KeyEnter, 0))
	req := pairingChatRequest(h, "reject")
	chatEqual(t, "wire retains scheme", req.PairingTarget, key)
}

func TestPairUXAcceptanceAmbiguousShortKeyNeverDecides(t *testing.T) {
	h := pairingChatHarness(t, true)
	prefix := strings.Repeat("a", 12)
	pairingChatList(h, []string{"sha256:" + prefix + strings.Repeat("b", 52), "sha256:" + prefix + strings.Repeat("c", 52)}, 2)
	v2Command(h, "/pair accept "+prefix)
	chatEqual(t, "collision never chooses first request", h.m.State().InFlight, 0)
	chatEqual(t, "collision does not queue", h.m.State().Queued, 0)
}

func TestPairUXAcceptanceContactLabelsKeepStableIdentities(t *testing.T) {
	key := "sha256:" + strings.Repeat("d", 64)
	for _, tc := range []struct{ name, destination, label string }{
		{" Home Mac ", "100.64.0.2:7445", "Home Mac"},
		{"workspace", "100.64.0.2:7445", "100.64.0.2"},
		{"", "[fd00::1]:7445", "fd00::1"},
		{"   ", "", strings.Repeat("d", 12)},
	} {
		t.Run(tc.label, func(t *testing.T) {
			row := contactChatRow(key, tc.name, true)
			row.Destination = tc.destination
			h := contactChatHarness(t, row)
			views := contactChatViews(h)
			chatEqual(t, "one contact", len(views), 1)
			chatEqual(t, "label shown", views[0].Label, "⇄ "+tc.label)
			chatEqual(t, "identity unchanged", views[0].Contact, key)
			h.input(chatKey(tea.KeyTab, 0))
			req := h.take(protocol.TypeOperatorContact)
			page := contactMessagesPage(key)
			page.ContactPage.Sync.Dials = tc.destination != ""
			h.reply(req, page)
			h.await(func() bool { return h.m.State().InFlight == 0 })
			v3Contains(t, ansi.Strip(h.m.View().Content), "⇄ "+tc.label)
			v3Contains(t, h.m.State().Hint, "to ⇄ "+tc.label)
			if tc.destination != "" {
				v3Contains(t, h.m.State().Hint, "sent at once")
			} else {
				v3Contains(t, h.m.State().Hint, "leaves when they next sync")
			}
			v2Type(h, "hello")
			h.input(chatKey(tea.KeyEnter, 0))
			sent := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "send addresses full identity", sent.ContactKey, key)
		})
	}
}

func TestPairUXAcceptanceSameDisplayNameNeverMergesContacts(t *testing.T) {
	first := "sha256:" + strings.Repeat("e", 64)
	second := "sha256:" + strings.Repeat("f", 64)
	h := contactChatHarness(t, contactChatRow(first, "same name", false), contactChatRow(second, "same name", false))
	views := contactChatViews(h)
	chatEqual(t, "same names retain two contacts", len(views), 2)
	chatEqual(t, "names are display only", views[0].Label, views[1].Label)
	if views[0].ID == views[1].ID || views[0].Contact == views[1].Contact {
		t.Fatal("display name replaced identity")
	}
}

func TestPairUXAcceptancePeerNameCannotInjectTerminalControls(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	h := contactChatHarness(t, contactChatRow(key, "operator\x1b[2J\nforged", false))
	h.input(chatKey(tea.KeyTab, 0))
	if strings.Contains(h.m.View().Content, "\x1b[2J") {
		t.Fatal("contact label injected terminal control")
	}
	chatEqual(t, "untrusted label preserves identity", contactChatViews(h)[0].Contact, key)
}

func TestPairUXAcceptancePickerCandidatesResolveAcrossCollisionsAndPartialLists(t *testing.T) {
	prefix := strings.Repeat("a", 12)
	first := prefix + strings.Repeat("b", 52)
	second := prefix + strings.Repeat("c", 52)
	for _, tc := range []struct {
		name       string
		partial    bool
		otherState string
		want       []string
		selected   int
	}{
		{"pending collision", false, "pending_acceptance", []string{prefix + "b", prefix + "c"}, 0},
		{"pending collision second", false, "pending_acceptance", []string{prefix + "b", prefix + "c"}, 1},
		{"accepted collision", false, "accepted", []string{prefix + "b"}, 0},
		{"rejected collision", false, "rejected", []string{prefix + "b"}, 0},
		{"partial listing", true, "pending_acceptance", []string{first, second}, 0},
		{"partial listing second", true, "pending_acceptance", []string{first, second}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := pairingChatHarness(t, true)
			pairUXRequests(h, []protocol.PairingRequestView{
				{IdentityKey: "sha256:" + first, Name: "first", State: "pending_acceptance"},
				{IdentityKey: "sha256:" + second, Name: "second", State: tc.otherState},
			}, tc.partial)
			commandCompleteLine(h, "pair accept ")
			commandCompleteItems(t, h, tc.want)
			h.input(tea.WindowSizeMsg{Width: 45, Height: 16})
			v2FrameFits(t, h, 45, 16)
			h.input(tea.WindowSizeMsg{Width: 120, Height: 30})
			for i := 0; i < tc.selected; i++ {
				h.input(chatKey(tea.KeyDown, 0))
			}
			h.input(chatKey(tea.KeyEnter, 0))
			chatEqual(t, "picker choice only completes", h.m.State().InFlight, 0)
			chatEqual(t, "offered identity visible", h.m.State().CommandLine.Text, "pair accept "+tc.want[tc.selected]+" ")
			h.input(chatKey(tea.KeyEnter, 0))
			req := pairingChatRequest(h, "accept")
			chatEqual(t, "offered prefix resolves to intended full key", req.PairingTarget, "sha256:"+[]string{first, second}[tc.selected])
		})
	}
}
