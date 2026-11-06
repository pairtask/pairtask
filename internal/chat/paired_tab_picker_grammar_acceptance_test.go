package chat_test

import (
	"slices"
	"strings"
	"testing"
)

func pairedTabRejectChoices(t *testing.T, h *chatHarness, bad ...string) {
	t.Helper()
	p := h.m.State().Picker
	if p == nil {
		return
	}
	for _, token := range bad {
		if slices.Contains(p.Items, token) {
			t.Errorf("unusable or duplicate recipient offered: %q in %q", token, p.Items)
		}
		if strings.Contains(pairedTabView(h), "@"+token) {
			t.Errorf("unusable or duplicate recipient rendered: %q", token)
		}
	}
}

func TestPairedTabPickerAcceptanceLocalChoicesUseMentionGrammar(t *testing.T) {
	h, _ := sharePickerHarness(t, "id with spaces", "@literal", "valid-worker")
	v2Type(h, "@")
	pairedTabRejectChoices(t, h, "id with spaces@here", "@literal@here")
	v3Contains(t, pairedTabView(h), "@valid-worker@here")
	pairedTabPick(t, h, "valid-worker@here")
	chatEqual(t, "valid local token inserted exactly", h.m.State().Draft, "@valid-worker@here ")
}

func TestPairedTabPickerAcceptanceRemoteChoicesUseMentionGrammar(t *testing.T) {
	for _, draft := range []string{"@", "@bad@", "@valid-remote@"} {
		t.Run(draft, func(t *testing.T) {
			h, key := pairedTabCapabilityHarness(t, true)
			contactRecipientRefresh(t, h, key, "id with spaces", "@literal", "bad@name", "valid-remote")
			v2Type(h, draft)
			pairedTabRejectChoices(t, h, "id with spaces", "@literal", "bad@name", "bad@name@Office")
			if draft == "@bad@" {
				return
			}
			token := "valid-remote"
			if draft != "@" {
				token += "@Office"
			}
			v3Contains(t, pairedTabView(h), "@valid-remote@Office")
			pairedTabPick(t, h, token)
			chatEqual(t, "valid remote token inserted exactly", h.m.State().Draft, "@"+token+" ")
		})
	}
}

func TestPairedTabPickerAcceptanceCompletedAliasesHideOnlyTheirOwnIdentity(t *testing.T) {
	for _, tc := range []struct {
		draft, duplicate, otherSide string
	}{
		{"@home-a@Office @h", "home-a", "home-a@here"},
		{"@home-a @home-a@", "home-a@Office", "home-a@here"},
		{"@operator@here @op", "operator", "operator@Office"},
		{"@operator @op", "operator", "operator@Office"},
	} {
		t.Run(tc.draft, func(t *testing.T) {
			h, _ := pairedTabHarness(t)
			v2Type(h, tc.draft)
			p := h.m.State().Picker
			if p == nil {
				t.Fatal("other side's distinct identity disappeared")
			}
			if slices.Contains(p.Items, tc.duplicate) {
				t.Errorf("resolved identity offered again through alias: %q in %q", tc.duplicate, p.Items)
			}
			if !slices.Contains(p.Items, tc.otherSide) {
				t.Errorf("different side's identity missing: %q in %q", tc.otherSide, p.Items)
			}
			chatEqual(t, "deduplicating suggestions preserves draft", h.m.State().Draft, tc.draft)
			chatEqual(t, "deduplicating suggestions sends nothing", h.m.State().InFlight, 0)
		})
	}
}
