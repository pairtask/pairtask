package chat_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func agentClaimsSnapshot() protocol.Message {
	st := agentStatusDeclaration("active")
	st.AgentID = "alpha"
	snap := agentStatusSnapshot(1, st)
	snap.Snapshot.Agents = []protocol.AgentPresence{{AgentID: "alpha", SessionID: "alpha-new", ProtocolVersion: protocol.Version}}
	snap.Snapshot.AgentsTotal = 1
	var claims []protocol.Claim
	for i, state := range []string{"active", "done", "released"} {
		claims = append(claims, protocol.Claim{OperatorMessageID: "task-parent", Task: []string{"build", "verify", "abandoned"}[i], Topic: "visible claim topic", Role: "owner", AgentID: "alpha", State: state, ClaimedAt: time.Now().Add(-time.Hour)})
	}
	snap.Snapshot.Claims = []protocol.ClaimSet{{OperatorMessageID: "task-parent", ClaimPage: protocol.ClaimPage{Claims: claims, Total: 3}}}
	snap.Snapshot.ClaimsTotal = 1
	return snap
}

func agentClaimsPanel(t *testing.T, snap protocol.Message, parent string) (*chatHarness, string) {
	t.Helper()
	h := claimChatHarness(t, snap, parent, 7)
	h.input(tea.WindowSizeMsg{Width: 120, Height: 50})
	v2Command(h, "/agents")
	return h, ansi.Strip(h.m.View().Content)
}

func TestAgentClaimsSurfacesAcceptancePanelShowsHeldClaimsAndParent(t *testing.T) {
	h, panel := agentClaimsPanel(t, agentClaimsSnapshot(), "task-parent")
	for _, want := range []string{"build", "verify", "owner", "active", "done", "#7", "visible claim topic"} {
		v3Contains(t, panel, want)
	}
	if strings.Contains(panel, "abandoned") {
		t.Fatal("released claim appeared in the agent summary")
	}
	chatEqual(t, "claims do not invent messages", len(h.m.State().Entries), 1)
	chatEqual(t, "claims do not send", h.m.State().Intent, nil)
	chatEqual(t, "claims do not acknowledge", len(h.m.State().QueuedAcks), 0)
}

func TestAgentClaimsSurfacesAcceptanceNoStatusStillShowsClaims(t *testing.T) {
	snap := agentClaimsSnapshot()
	snap.Snapshot.AgentStatus, snap.Snapshot.AgentStatusTotal = nil, 0
	_, panel := agentClaimsPanel(t, snap, "unloaded-parent")
	for _, want := range []string{"alpha", "build", "task-par", "no status declared"} {
		v3Contains(t, panel, want)
	}
}

func TestAgentClaimsSurfacesAcceptancePartialStatusesDoNotHideLoadedClaims(t *testing.T) {
	snap := agentClaimsSnapshot()
	snap.Snapshot.AgentStatus = nil
	snap.Snapshot.AgentStatusTotal, snap.Snapshot.AgentStatusTruncated = 80, true
	_, panel := agentClaimsPanel(t, snap, "task-parent")
	v3Contains(t, panel, "build")
	v3Contains(t, panel, "#7")
	if strings.Contains(panel, "no status declared") {
		t.Fatal("missing declaration inferred from truncated statuses")
	}
}

func TestAgentClaimsSurfacesAcceptanceBothPartialLevelsAreVisible(t *testing.T) {
	for _, global := range []bool{false, true} {
		name := "per parent"
		if global {
			name = "global"
		}
		t.Run(name, func(t *testing.T) {
			snap := agentClaimsSnapshot()
			if global {
				snap.Snapshot.ClaimsTruncated, snap.Snapshot.ClaimsTotal = true, 40
			} else {
				snap.Snapshot.Claims[0].Truncated, snap.Snapshot.Claims[0].Total = true, 20
			}
			_, panel := agentClaimsPanel(t, snap, "task-parent")
			requirePartialClaimNotice(t, strings.ToLower(panel), []string{"partial", "truncat", "omitted"})
		})
	}
}

func TestAgentClaimsSurfacesAcceptanceHorizontalRuleBeforeDetails(t *testing.T) {
	_, panel := agentClaimsPanel(t, agentClaimsSnapshot(), "task-parent")
	lines := strings.Split(panel, "\n")
	presence, details := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "●") {
			presence = i
		}
		if details < 0 && strings.Contains(line, "◆") {
			details = i
		}
	}
	if presence < 0 || details <= presence {
		t.Fatalf("presence and details not found: %s", panel)
	}
	for _, line := range lines[presence+1 : details] {
		if strings.Contains(line, "───") {
			return
		}
	}
	t.Fatal("presence/details boundary needs a horizontal rule, not just a blank line")
}

func TestAgentClaimsSurfacesAcceptanceRefreshAndNarrowFrames(t *testing.T) {
	h, _ := agentClaimsPanel(t, agentClaimsSnapshot(), "task-parent")
	snap := agentClaimsSnapshot()
	snap.Snapshot.Watermark.Revision = 2
	snap.Snapshot.Claims[0].Claims[0].Task = "replacement"
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{Kind: protocol.EventClaimsChanged, OperatorMessageID: "task-parent", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	h.reply(h.take(protocol.TypeOperatorSnapshot), snap)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	panel := ansi.Strip(h.m.View().Content)
	v3Contains(t, panel, "replacement")
	if strings.Contains(panel, "build") {
		t.Fatal("agent summary retained a superseded claim")
	}
	for _, width := range []int{32, 60, 120} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 50})
		v2FrameFits(t, h, width, 50)
	}
}
