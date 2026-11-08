package chat_test

import (
	"strings"
	"testing"

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
	v2Command(h, "/version")
	v3Contains(t, ansi.Strip(h.m.View().Content), "pk version")
}
