package chat_test

import (
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
	"strings"
	"testing"
)

func TestConsentPreviewAcceptanceReviewDisplaysPinsAndPublicationCeiling(t *testing.T) {
	h := consentInputHarness(t)
	page := consentInputPage()
	page.ConsentPage.Items[0].Review = &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "293"}, Scope: "security", Base: "full-immutable-base-sha-293", Head: "full-immutable-head-sha-293", Publish: true, Formal: true}
	page.ConsentPage.Items[0].Targets = []protocol.ConsentTarget{{Agent: "alpha", Operations: []string{"review", "publish"}}}
	consentInputOpen(t, h, page)
	frame := ansi.Strip(h.m.View().Content)
	for _, value := range []string{"github", "pairtask/pairtask", "293", "security", "full-immutable-base-sha-293", "full-immutable-head-sha-293", "review", "publish", "formal"} {
		if !strings.Contains(strings.ToLower(frame), value) {
			t.Errorf("review preview omitted pinned scope %q: %s", value, frame)
		}
	}
	consentInputNoWrites(t, h)
}

func TestConsentPreviewAcceptanceRemoteControlSequencesRemainData(t *testing.T) {
	h := consentInputHarness(t)
	page := consentInputPage()
	page.ConsentPage.Items[0].Text = consentFixtureText + "\x1b]52;c;c2VjcmV0\x07" + "\x1b[2J"
	consentInputOpen(t, h, page)
	raw := h.m.View().Content
	if strings.Contains(raw, "\x1b]52;") || strings.Contains(raw, "\x1b[2J") {
		t.Fatal("remote preview emitted terminal clipboard or screen-control sequence")
	}
	consentInputNoWrites(t, h)
}
