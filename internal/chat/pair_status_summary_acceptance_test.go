package chat_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func pairSummaryView(t *testing.T, c protocol.PairingContactView) (*chatHarness, string) {
	t.Helper()
	h := pairingChatHarness(t, true)
	h.input(tea.WindowSizeMsg{Width: 160, Height: 60})
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	h.reply(req, contactChatReply(c))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h, ansi.Strip(h.m.View().Content)
}

func TestPairStatusSummaryAcceptanceHonestConnectionStates(t *testing.T) {
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		name      string
		confirmed bool
		summary   *protocol.PairingContactSummary
		want      string
	}{
		{"missing", true, nil, "unknown"},
		{"live", true, &protocol.PairingContactSummary{Sync: protocol.ContactSyncView{Live: true}}, "connected"},
		{"unconfirmed", false, &protocol.PairingContactSummary{}, "pending"},
		{"failed", true, &protocol.PairingContactSummary{Sync: protocol.ContactSyncView{LastAttempt: &at, LastFailure: "dial timeout", Failures: 2}}, "last attempt failed"},
		{"idle", true, &protocol.PairingContactSummary{Sync: protocol.ContactSyncView{LastSuccess: &at}}, "idle"},
		{"never", true, &protocol.PairingContactSummary{}, "not yet observed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := contactChatRow("sha256:"+strings.Repeat("a", 64), "Remote", tc.confirmed)
			c.Summary = tc.summary
			h, view := pairSummaryView(t, c)
			v3Contains(t, view, tc.want)
			chatEqual(t, "summary needs no contact fetch", h.m.State().InFlight, 0)
			if tc.name == "idle" || tc.name == "never" || tc.name == "missing" {
				if strings.Contains(view, "offline") {
					t.Fatal("lack of a live link was presented as offline")
				}
			}
		})
	}
}

func TestPairStatusSummaryAcceptanceReviewsVisibleBeforeOpeningContact(t *testing.T) {
	c := contactChatRow("sha256:"+strings.Repeat("b", 64), "Office", true)
	c.Summary = &protocol.PairingContactSummary{ReviewsAwaiting: 3}
	h, view := pairSummaryView(t, c)
	v3Contains(t, view, "Waiting for you")
	v3Contains(t, view, "3 review requests")
	v3Contains(t, view, "Office")
	paired, waiting := strings.Index(view, "Paired workspaces"), strings.Index(view, "Waiting for you")
	if paired < 0 || waiting <= paired {
		t.Fatal("paired workspaces must appear before waiting decisions")
	}
	chatEqual(t, "contact never opened", h.m.State().SelectedAll, true)
	chatEqual(t, "one snapshot suffices", h.m.State().InFlight, 0)
}

func TestPairStatusSummaryAcceptanceFailedRefreshKeepsSnapshot(t *testing.T) {
	c := contactChatRow("sha256:"+strings.Repeat("c", 64), "Retained workspace", true)
	h, _ := pairSummaryView(t, c)
	h.input(chatKey(tea.KeyEscape, 0))
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	h.reply(req, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrMalformedFrame, Detail: "summary unavailable"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	for _, text := range []string{"Retained workspace", "couldn't refresh", "showing", "summary unavailable"} {
		v3Contains(t, view, text)
	}
	if strings.Contains(view, "no paired workspaces yet") {
		t.Fatal("failed refresh presented as an empty successful snapshot")
	}
	h.input(chatKey(tea.KeyEscape, 0))
	v2Command(h, "/pair status")
	req = pairingChatRequest(h, "status")
	h.reply(req, protocol.Message{Type: protocol.TypeOperatorPairingResult, PairingResult: &protocol.PairingSnapshot{}})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view = ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "no paired workspaces yet")
	for _, stale := range []string{"Retained workspace", "couldn't refresh", "summary unavailable"} {
		if strings.Contains(view, stale) {
			t.Fatalf("successful empty refresh retained stale information %q", stale)
		}
	}
}

func TestPairStatusSummaryAcceptancePartialContactsDoNotClaimCompleteCounts(t *testing.T) {
	h := pairingChatHarness(t, true)
	h.input(tea.WindowSizeMsg{Width: 160, Height: 60})
	c := contactChatRow("sha256:"+strings.Repeat("d", 64), "Listed workspace", true)
	c.Summary = &protocol.PairingContactSummary{ReviewsAwaiting: 2}
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	response := contactChatReply(c)
	response.PairingResult.ContactsTotal = 3
	response.PairingResult.ContactsTruncated = true
	h.reply(req, response)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := ansi.Strip(h.m.View().Content)
	v3Contains(t, view, "partial")
	v3Contains(t, view, "listed contacts")
	v3Contains(t, view, "2 review requests")
}

func TestPairStatusSummaryAcceptanceActionSectionsBeforeHistory(t *testing.T) {
	h := pairingChatHarness(t, true)
	h.input(tea.WindowSizeMsg{Width: 180, Height: 70})
	c := contactChatRow("sha256:"+strings.Repeat("e", 64), "Paired office", true)
	c.Summary = &protocol.PairingContactSummary{ReviewsAwaiting: 1}
	response := contactChatReply(c)
	s := response.PairingResult
	s.SelfName = "My workspace"
	s.Requests = []protocol.PairingRequestView{{IdentityKey: "sha256:" + strings.Repeat("f", 64), Name: "New request", State: "pending_acceptance"}}
	s.RequestsTotal = 1
	s.Joins = []protocol.PairingJoinView{{OperationID: "waiting", State: "awaiting_acceptance"}, {OperationID: "old-failure", State: "failed"}}
	s.JoinsTotal = 2
	s.Invitation = &protocol.InvitationView{ID: "open-invitation", ExpiresAt: time.Now().Add(time.Hour)}
	v2Command(h, "/pair status")
	req := pairingChatRequest(h, "status")
	h.reply(req, response)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	view := strings.ToLower(ansi.Strip(h.m.View().Content))
	previous := -1
	for _, section := range []string{"paired workspaces", "waiting for you", "waiting for them", "invitation", "this workspace", "history"} {
		at := strings.Index(view, section)
		if at < 0 || at <= previous {
			t.Fatalf("section %q missing or out of order in %s", section, view)
		}
		previous = at
	}
}
