package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestCommandEnterAcceptanceIncompleteCommandsKeepInputAndOfferChoices(t *testing.T) {
	for _, line := range []string{"room", "rooms", "room add", "room remove", "room delete", "room add alpha", "room remove alpha", "chat", "review", "review owner/repo#1", "ack from"} {
		t.Run(line, func(t *testing.T) {
			h := commandCompleteHarness(t)
			v2Type(h, "unsent draft")
			v2Command(h, "/"+line)
			cmd := h.m.State().CommandLine
			if cmd == nil || strings.TrimSpace(cmd.Text) != line {
				t.Fatalf("incomplete command lost: %+v", cmd)
			}
			if p := h.m.State().Picker; p == nil || len(p.Items) == 0 {
				t.Fatalf("missing choices for %q", line)
			}
			chatEqual(t, "incomplete command sends nothing", h.m.State().InFlight, 0)
			chatEqual(t, "message draft retained", h.m.State().Draft, "unsent draft")
		})
	}
}

func TestCommandEnterAcceptanceMissingFreeTextKeepsInputAndHelp(t *testing.T) {
	for _, line := range []string{"diagram", "copy", "stages", "claims", "pair redeem"} {
		t.Run(line, func(t *testing.T) {
			h := commandCompleteHarness(t)
			v2Command(h, "/"+line)
			cmd := h.m.State().CommandLine
			if cmd == nil || strings.TrimSpace(cmd.Text) != line {
				t.Fatalf("incomplete command discarded: %+v", cmd)
			}
			chatEqual(t, "missing argument never sends", h.m.State().InFlight, 0)
			if strings.TrimSpace(h.m.State().Status) == "" {
				t.Fatal("missing argument has no explanation")
			}
		})
	}
}

func TestCommandEnterAcceptanceValidNoArgumentCommandStillExecutes(t *testing.T) {
	h := commandCompleteHarness(t)
	v2Command(h, "/help")
	chatEqual(t, "valid command executes", h.m.State().CommandLine == nil, true)
	h.input(chatKey(tea.KeyEscape, 0))
}
