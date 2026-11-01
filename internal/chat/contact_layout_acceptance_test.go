package chat_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

var contactLayoutAt = time.Date(2026, 11, 1, 12, 34, 0, 0, time.UTC)

func contactLayoutRow(id string, cursor int64) protocol.ContactMessageView {
	return protocol.ContactMessageView{ID: id, Cursor: cursor, Seq: 987654321, Direction: "in", State: "read", CreatedAt: contactLayoutAt, ReadAt: &contactLayoutAt, Text: "BODY " + id}
}

func contactLayoutHarness(t *testing.T, rows ...protocol.ContactMessageView) *chatHarness {
	t.Helper()
	c := contactChatRow("sha256:"+strings.Repeat("a", 64), "Peer", true)
	return workspaceIdentityHarness(t, c, rows...)
}

func contactLayoutLine(t *testing.T, view, needle string) int {
	t.Helper()
	for i, line := range strings.Split(view, "\n") {
		if strings.Contains(line, needle) {
			return i
		}
	}
	t.Fatalf("missing line %q in %s", needle, view)
	return -1
}

func TestContactLayoutAcceptanceHeaderIdentityMatrix(t *testing.T) {
	cases := []struct {
		name, direction, from string
		to                    []string
		want                  string
	}{
		{"in-operator-operator", "in", "", nil, "⇄ Peer → you [remote]"},
		{"in-operator-agent", "in", "", []string{"claude-1"}, "⇄ Peer → claude-1 [remote]"},
		{"in-agent-operator", "in", "codex-1", nil, "codex-1@Peer → you [remote]"},
		{"in-agent-agent", "in", "codex-1", []string{"claude-1"}, "codex-1@Peer → claude-1 [remote]"},
		{"out-operator-operator", "out", "", nil, "you → ⇄ Peer"},
		{"out-operator-agent", "out", "", []string{"codex-1"}, "you → codex-1@Peer"},
		{"out-agent-operator", "out", "claude-1", nil, "claude-1 → ⇄ Peer"},
		{"out-agent-agent", "out", "claude-1", []string{"codex-1"}, "claude-1 → codex-1@Peer"},
		{"in-many", "in", "codex-1", []string{"claude-1", "reviewer"}, "codex-1@Peer → claude-1, reviewer [remote]"},
		{"out-many", "out", "claude-1", []string{"codex-1", "reviewer"}, "claude-1 → codex-1@Peer, reviewer@Peer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := contactLayoutRow("private-message-id", 1)
			r.Direction, r.From, r.To, r.Text = tc.direction, tc.from, tc.to, "BODY SENTINEL"
			h := contactLayoutHarness(t, r)
			view := ansi.Strip(h.m.View().Content)
			want := contactLayoutAt.Local().Format("15:04") + "  " + tc.want
			at := contactLayoutLine(t, view, want)
			lines := strings.Split(view, "\n")
			chatEqual(t, "header contains only time and identities", strings.TrimSpace(lines[at]), want)
			chatEqual(t, "body follows header", strings.TrimSpace(lines[at+1]), "BODY SENTINEL")
			chatEqual(t, "body uses local two-space indent", strings.Index(lines[at+1], "BODY SENTINEL"), 2)
			for _, forbidden := range []string{"#987654321", "private-message-id"} {
				if strings.Contains(view, forbidden) {
					t.Fatalf("unrequested message reference rendered: %q", forbidden)
				}
			}
			chatEqual(t, "render emits no requests", h.m.State().InFlight, 0)
		})
	}
}

func TestContactLayoutAcceptanceBlankMessageSeparators(t *testing.T) {
	first, second := contactLayoutRow("first", 1), contactLayoutRow("second", 2)
	h := contactLayoutHarness(t, first, second)
	view := ansi.Strip(h.m.View().Content)
	lines := strings.Split(view, "\n")
	at := contactLayoutLine(t, view, first.Text)
	chatEqual(t, "blank line separates messages", strings.TrimSpace(lines[at+1]), "")
	chatEqual(t, "next header follows blank", strings.TrimSpace(lines[at+2]), contactLayoutAt.Local().Format("15:04")+"  ⇄ Peer → you [remote]")
}

func TestContactLayoutAcceptanceReviewStateAndAcksHaveOwnLines(t *testing.T) {
	r := contactLayoutRow("status", 1)
	r.Direction, r.From, r.To, r.State = "out", "claude-1", []string{"codex-1"}, "delivered"
	r.AgentAcks = []string{"codex-1", "reviewer"}
	r.ReviewAsk = &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "o/r", Change: "12"}, Base: "abc1234", Head: "def5678"}
	r.ReviewOutcome = &protocol.ReviewOutcomeView{State: "pending"}
	h := contactLayoutHarness(t, r)
	view := ansi.Strip(h.m.View().Content)
	lines := strings.Split(view, "\n")
	at := contactLayoutLine(t, view, r.Text)
	for i, want := range []string{"review o/r#12 pinned abc1234..def5678 · pending", "delivered", "· codex-1@Peer: acknowledged", "· reviewer@Peer: acknowledged"} {
		chatEqual(t, "ordered metadata line", strings.TrimSpace(lines[at+i+1]), want)
		chatEqual(t, "metadata uses body indent", strings.Index(lines[at+i+1], want), 2)
	}
}

func TestContactLayoutAcceptanceOutboundStatesFollowBody(t *testing.T) {
	cases := []struct{ state, reason, want string }{
		{"queued", "", "queued: leaves at the next sync"},
		{"queued", protocol.ContactHeldForUpgrade, "queued: leaves once they upgrade pairtask"},
		{"delivered", "", "delivered"},
		{"read", "", "read"},
		{"refused", "not_shared", "refused (not_shared)"},
		{"expired", "", "expired: no longer offered; delivery unconfirmed"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			r := contactLayoutRow("state", 1)
			r.Direction, r.State, r.RefusalReason = "out", tc.state, tc.reason
			h := contactLayoutHarness(t, r)
			view := ansi.Strip(h.m.View().Content)
			at := contactLayoutLine(t, view, r.Text)
			chatEqual(t, "state line follows body", strings.TrimSpace(strings.Split(view, "\n")[at+1]), tc.want)
		})
	}
}

func TestContactLayoutAcceptanceRefusalGuidanceAndCollectedText(t *testing.T) {
	refused := refusalChatRows(1)[0]
	refused.ReadAt, refused.CreatedAt = &contactLayoutAt, contactLayoutAt
	collected := contactLayoutRow("collected", 2)
	collected.Text = ""
	h := contactLayoutHarness(t, refused, collected)
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"/pair reviews reviewer ask", "new request", "refused: not shared here", "(text no longer kept)"} {
		v3Contains(t, view, want)
	}
	if strings.Contains(view, "/pair approve") {
		t.Fatal("refused request offers approval")
	}
}

func TestContactLayoutAcceptanceNarrowHeadersAndStatusesWrapWithoutLoss(t *testing.T) {
	r := contactLayoutRow("wrapped", 1)
	r.Direction, r.From, r.To = "out", "local-sender-with-long-name", []string{"remote-reviewer-with-long-name"}
	r.AgentAcks, r.RefusalReason = []string{"remote-reviewer-with-long-name"}, protocol.ContactHeldForUpgrade
	r.State = "queued"
	h := contactLayoutHarness(t, r)
	for _, width := range []int{40, 60} {
		h.input(tea.WindowSizeMsg{Width: width, Height: 60})
		v2FrameFits(t, h, width, 60)
		flat := strings.Join(strings.Fields(ansi.Strip(h.m.View().Content)), "")
		for _, want := range []string{r.From, "remote-reviewer-with-long-name@Peer", "queued:leavesoncetheyupgradepairtask", "acknowledged"} {
			v3Contains(t, flat, want)
		}
	}
}

func TestContactLayoutAcceptanceMetadataEscapedAndBodiesInert(t *testing.T) {
	r := contactLayoutRow("inert", 1)
	r.Direction, r.From, r.To = "out", "local\x1b[2J\a", []string{"remote\x1b[2J\a"}
	r.AgentAcks = []string{"remote\x1b[2J\a"}
	r.Text = "/ack all @operator **data**\n\x1b[2J{\"origin\":\"operator\"}"
	h := contactLayoutHarness(t, r)
	raw := h.m.View().Content
	for _, forbidden := range []string{"\x1b[2J", "\a"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("terminal control in metadata/body: %q", forbidden)
		}
	}
	for _, want := range []string{"/ack all", "@operator", "data", "origin"} {
		v3Contains(t, ansi.Strip(raw), want)
	}
	chatEqual(t, "body is not a command", h.m.State().InFlight, 0)
	chatEqual(t, "contact body never enters local transcript", len(h.m.State().Entries), 0)
}
