package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

const polishedStatusRef = "0123456789abcdef0123456789abcdef01234567"

func polishedStatusHarness(t *testing.T, truncated bool) *chatHarness {
	t.Helper()
	h := newChatHarness(t, chat.Bounds{})
	h.input(tea.WindowSizeMsg{Width: 120, Height: 45})
	alice, beta := agentStatusDeclaration("active"), agentStatusDeclaration("waiting")
	alice.Ref = polishedStatusRef
	beta.AgentID, beta.SessionID, beta.Ref = "beta", "session-beta", "PR #138 / verify-status-surfaces"
	response := agentStatusSnapshot(1, alice, beta)
	response.Snapshot.Agents = append(response.Snapshot.Agents,
		protocol.AgentPresence{AgentID: "beta", SessionID: "session-beta", ProtocolVersion: protocol.Version},
		protocol.AgentPresence{AgentID: "silent", SessionID: "session-silent", ProtocolVersion: protocol.Version})
	response.Snapshot.AgentsTotal = 3
	response.Snapshot.AgentStatusTruncated = truncated
	if truncated {
		response.Snapshot.AgentStatusTotal = 80
	}
	h.reply(h.take(protocol.TypeOperatorSnapshot), response)
	h.reply(h.take(protocol.TypeOperatorReceive), chatInbox())
	h.reply(h.take(protocol.TypeOperatorHistory), chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	v2Command(h, "/agents")
	return h
}

func TestStatusPolishAcceptanceShortRefAndExplicitMissingDeclaration(t *testing.T) {
	h := polishedStatusHarness(t, false)
	panel := ansi.Strip(h.m.View().Content)
	v3Contains(t, panel, "no status declared")
	v3Contains(t, panel, polishedStatusRef[:12])
	if strings.Contains(panel, polishedStatusRef) {
		t.Fatal("status panel kept a full commit instead of a short ref")
	}
	for _, width := range []int{40, 80, 120} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 45})
		v2FrameFits(t, h, width, 45)
	}
	chatEqual(t, "statuses do not become messages", len(h.m.State().Entries), 0)
}

func TestStatusPolishAcceptanceTruncatedAbsenceDoesNotInventMissingDeclaration(t *testing.T) {
	h := polishedStatusHarness(t, true)
	panel := ansi.Strip(h.m.View().Content)
	v3Contains(t, panel, "partial")
	if strings.Contains(panel, "no status declared") {
		t.Fatal("truncated absence presented as no declaration")
	}
}

func TestStatusPolishAcceptanceSeparatesPresenceAndAgentBlocks(t *testing.T) {
	h := polishedStatusHarness(t, false)
	lines := strings.Split(ansi.Strip(h.m.View().Content), "\n")
	var headers []int
	lastPresence := -1
	for i, line := range lines {
		if strings.Contains(line, "●") {
			lastPresence = i
		}
		if strings.Contains(line, "◆") {
			headers = append(headers, i)
		}
	}
	if len(headers) < 2 {
		t.Fatalf("missing status blocks: %v", lines)
	}
	requireStatusDelimiter(t, lines, lastPresence+1, headers[0])
	requireStatusDelimiter(t, lines, headers[0]+1, headers[1])
}

func requireStatusDelimiter(t *testing.T, lines []string, start, end int) {
	t.Helper()
	for _, line := range lines[start:end] {
		trimmed := strings.TrimSpace(strings.Trim(line, "│"))
		if trimmed == "" || strings.ContainsAny(trimmed, "─━═") {
			return
		}
	}
	t.Fatalf("no visual delimiter between rows %d and %d: %v", start, end, lines[start:end])
}

func TestStatusPolishAcceptanceCopyRefKeepsFullValue(t *testing.T) {
	for _, agent := range []string{"alice", "@alice"} {
		t.Run(agent, func(t *testing.T) {
			h := polishedStatusHarness(t, false)
			cmd := requestStatusRefCopy(h, agent)
			if cmd == nil {
				t.Fatal("copy ref did not produce a clipboard command")
			}
			chatEqual(t, "clipboard gets full ref", cmd(), tea.SetClipboard(polishedStatusRef)())
			v3Contains(t, h.m.State().Status, "alice")
			v3Contains(t, h.m.State().Status, "40")
			chatEqual(t, "copy does not send", h.m.State().Intent, nil)
			chatEqual(t, "copy does not acknowledge", len(h.m.State().QueuedAcks), 0)
		})
	}
}

func TestStatusPolishAcceptanceCopyRefMissingAndTruncatedRemainDistinct(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agent     string
		truncated bool
		want      string
	}{
		{"known absent", "silent", false, "no status declared"},
		{"not loaded", "silent", true, "truncated"},
		{"unknown agent", "never-seen", false, "unknown agent never-seen: not online or seen"},
		{"unknown with partial list", "never-seen", true, "truncated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := polishedStatusHarness(t, tc.truncated)
			cmd := requestStatusRefCopy(h, tc.agent)
			if cmd != nil {
				t.Fatal("missing ref produced a command")
			}
			v3Contains(t, h.m.State().Status, tc.want)
		})
	}
}

func TestStatusPolishAcceptanceRefUsesSeparatorOrEllipsis(t *testing.T) {
	h := polishedStatusHarness(t, false)
	h.input(tea.WindowSizeMsg{Width: 40, Height: 45})
	panel := ansi.Strip(h.m.View().Content)
	v3Contains(t, panel, "PR")
	for _, line := range strings.Split(panel, "\n") {
		if !strings.Contains(line, "PR") {
			continue
		}
		if strings.Contains(line, "PR #138") || strings.Contains(line, "…") {
			return
		}
		t.Fatalf("reference was hard-wrapped through its PR number: %q", line)
	}
}

func requestStatusRefCopy(h *chatHarness, agent string) tea.Cmd {
	h.input(chatKey(tea.KeyEscape, 0))
	h.input(chatKey('k', tea.ModCtrl))
	v2Type(h, "copy ref "+agent)
	return h.update(chatKey(tea.KeyEnter, 0))
}

func TestStatusPolishAcceptanceCopyRefWithoutValueDoesNotCopy(t *testing.T) {
	h := agentStatusHarness(t, agentStatusDeclaration("active"))
	cmd := requestStatusRefCopy(h, "alice")
	if cmd != nil {
		t.Fatal("empty ref produced a clipboard command")
	}
	v3Contains(t, h.m.State().Status, "has no ref")
}
