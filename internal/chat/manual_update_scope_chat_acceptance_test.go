package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestManualUpdateScopeChatAcceptanceCustomTargetUsesHostGuidance(t *testing.T) {
	notice := manualUpdateNotice("update_available", "upgrade_app")
	notice["scope"] = "custom"
	h := manualUpdateHarness(t, true, notice)
	frame := ansi.Strip(h.m.View().Content)
	v3Contains(t, frame, "pk version")
	if strings.Contains(frame, "host: pk upgrade") {
		t.Fatal("custom deployment offered an upgrade against the default target")
	}
	h.input(tea.WindowSizeMsg{Width: 260, Height: 32})
	v2Command(h, "/version")
	version := ansi.Strip(h.m.View().Content)
	for _, word := range []string{"pk version", "--project", "--remote", "--repo"} {
		v3Contains(t, version, word)
	}
}
