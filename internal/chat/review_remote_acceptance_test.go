package chat_test

import (
	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
	"slices"
	"strings"
	"testing"
)

func TestReviewRemoteAcceptanceCompletionOffersQualifiedPairedReviewer(t *testing.T) {
	h, _ := sharePickerHarness(t, "local-only")
	commandCompleteLine(h, "review --no-publish pairtask/pairtask#295 @rem")
	picker := h.m.State().Picker
	if picker == nil || !slices.Contains(picker.Items, "@remote-only@Office") {
		t.Fatalf("review completion omitted qualified paired reviewer: %+v", picker)
	}
	chatEqual(t, "completion never submits review", h.m.State().InFlight, 0)
}

func TestReviewRemoteAcceptanceAliasAmbiguityNeverFallsBackToLocal(t *testing.T) {
	first := contactChatRow("sha256:"+strings.Repeat("a", 64), "Office", true)
	first.LocalName = "Home"
	second := contactChatRow("sha256:"+strings.Repeat("b", 64), "Home", true)
	h, lookup := remoteHarness(t, first, second)
	v2Command(h, "/review pairtask/pairtask#295 @reviewer@Home")
	chatEqual(t, "ambiguous workspace has no lookup", len(lookup.calls), 0)
	chatEqual(t, "ambiguous workspace never sends locally", h.m.State().InFlight, 0)
	v3Contains(t, h.m.State().Status, "several paired workspaces")
}

func remotePinnedCommand(reviewer string, n int) string {
	return "/review --pin " + strings.Repeat("1", n) + ".." + strings.Repeat("2", n) + " pairtask/pairtask#295 " + reviewer
}

func TestReviewRemoteAcceptanceQualifiedIdentityFormsAndQuotedAlias(t *testing.T) {
	for _, tc := range []struct{ token, alias, agent string }{
		{"@reviewer@Office", "", "reviewer"}, {"reviewer@Office", "", "reviewer"},
		{"@@literal@Office", "", "@literal"}, {"@agent@team@Office", "", "agent@team"},
		{"\"@reviewer@Office West\"", "Office West", "reviewer"},
		{"@reviewer@sha256:" + strings.Repeat("a", 12), "", "reviewer"},
	} {
		t.Run(tc.token, func(t *testing.T) {
			c := contactChatRow("sha256:"+strings.Repeat("a", 64), "Office", true)
			c.LocalName = tc.alias
			h, lookup := remoteHarness(t, c)
			v2Command(h, remotePinnedCommand(tc.token, 40))
			req := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "qualified reviewer resolves to canonical key", req.ContactKey, c.IdentityKey)
			if !slices.Equal(req.ContactTo, []string{tc.agent}) {
				t.Fatalf("qualified name changed agent identity: %v", req.ContactTo)
			}
			chatEqual(t, "identity parsing performs no lookup for override", len(lookup.calls), 0)
			remoteSettle(h, req)
		})
	}
}

func TestReviewRemoteAcceptanceSupportsSHA256PinOverrides(t *testing.T) {
	h, lookup := remoteHarness(t)
	v2Command(h, remotePinnedCommand("@reviewer@Office", 64))
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "64-character base retained", req.ReviewAsk.Base, strings.Repeat("1", 64))
	chatEqual(t, "64-character head retained", req.ReviewAsk.Head, strings.Repeat("2", 64))
	chatEqual(t, "64-character overrides bypass lookup", len(lookup.calls), 0)
	remoteSettle(h, req)
}

func TestReviewRemoteAcceptanceUnknownWorkspaceNeverFallsBack(t *testing.T) {
	h, lookup := remoteHarness(t)
	v2Command(h, "/review pairtask/pairtask#295 @reviewer@Unknown")
	v3Contains(t, h.m.State().Status, "no paired workspace Unknown")
	chatEqual(t, "unknown workspace never starts lookup", len(lookup.calls), 0)
	chatEqual(t, "unknown workspace never creates local review", h.m.State().InFlight, 0)
}

func TestReviewRemoteAcceptanceScopeFlagsRemainLiteral(t *testing.T) {
	h, _ := remoteHarness(t)
	v2Command(h, remotePinnedCommand("@reviewer@Office", 40)+" inspect @another --publish --formal")
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "scope preserves flag-like words", req.ReviewAsk.Scope, "inspect @another --publish --formal")
	chatEqual(t, "scope cannot enable publication", req.ReviewAsk.Publish, false)
	chatEqual(t, "scope cannot enable formal verdict", req.ReviewAsk.Formal, false)
	remoteSettle(h, req)
}

func TestReviewRemoteAcceptanceCompletionNeverUsesTruncatedDisplayLabel(t *testing.T) {
	name := strings.Repeat("long-workspace-", 5)
	c := contactChatRow("sha256:"+strings.Repeat("a", 64), name, true)
	h, _ := remoteHarness(t, c)
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	page := contactMessagesPage(c.IdentityKey)
	page.ContactPage.Directory = []string{"reviewer"}
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
	commandCompleteLine(h, "review pairtask/pairtask#295 @rev")
	p := h.m.State().Picker
	if p == nil || !slices.Contains(p.Items, "@reviewer@"+name) {
		t.Fatalf("full routing alias absent: %+v", p)
	}
	for _, item := range p.Items {
		if strings.Contains(item, "…") {
			t.Fatal("truncated display name used as route")
		}
	}
}

func remoteLoadDirectory(h *chatHarness, key string) {
	h.t.Helper()
	h.input(chatKey(tea.KeyTab, 0))
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(h.t, "directory belongs to intended contact", req.ContactKey, key)
	page := contactMessagesPage(key)
	page.ContactPage.Directory = []string{"agent"}
	h.reply(req, page)
	h.await(func() bool { return h.m.State().InFlight == 0 })
}

func remoteSelectCompletion(t *testing.T, h *chatHarness, key, fragment string) {
	t.Helper()
	commandCompleteLine(h, strings.TrimPrefix(remotePinnedCommand(fragment, 40), "/"))
	if p := h.m.State().Picker; p == nil || len(p.Items) != 1 {
		t.Fatalf("qualified completion must offer one usable reviewer: %+v", p)
	}
	h.input(chatKey(tea.KeyTab, 0))
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "selected completion routes to intended contact", req.ContactKey, key)
	if !slices.Equal(req.ContactTo, []string{"agent"}) {
		t.Fatalf("completion changed agent identity: %v", req.ContactTo)
	}
	remoteSettle(h, req)
}

func TestReviewRemoteAcceptanceCompletionSelectionRoundTripsQuotedAliases(t *testing.T) {
	for _, tc := range []struct{ name, fragment string }{
		{"Office West", "@ag"}, {"Office West", `"@ag`},
		{`Office "West"`, "@ag"}, {`Office\West`, "@ag"}, {"Office@West", "@ag"},
	} {
		t.Run(tc.name+tc.fragment, func(t *testing.T) {
			c := contactChatRow("sha256:"+strings.Repeat("a", 64), tc.name, true)
			h, _ := remoteHarness(t, c)
			remoteLoadDirectory(h, c.IdentityKey)
			remoteSelectCompletion(t, h, c.IdentityKey, tc.fragment)
		})
	}
}

func TestReviewRemoteAcceptanceKeyPrefixCompletionAvoidsOtherContactAlias(t *testing.T) {
	first := contactChatRow("sha256:"+strings.Repeat("a", 64), "Unsafe@Name", true)
	second := contactChatRow("sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("a", 12), true)
	h, _ := remoteHarness(t, first, second)
	remoteLoadDirectory(h, first.IdentityKey)
	commandCompleteLine(h, strings.TrimPrefix(remotePinnedCommand("@ag", 40), "/"))
	p := h.m.State().Picker
	if p == nil || !slices.Contains(p.Items, "@agent@sha256:"+strings.Repeat("a", 13)) {
		t.Fatalf("key-prefix completion did not avoid another contact's alias: %+v", p)
	}
	if slices.Contains(p.Items, "@agent@sha256:"+strings.Repeat("a", 12)) {
		t.Fatal("completion offered a key prefix that also names another contact")
	}
	h.input(chatKey(tea.KeyTab, 0))
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "collision-safe prefix routes correctly", req.ContactKey, first.IdentityKey)
	remoteSettle(h, req)
}

func TestReviewRemoteAcceptanceLocalReviewPolicyDefaultsRemainCompatible(t *testing.T) {
	for _, tc := range []struct {
		flags           string
		publish, formal bool
	}{
		{"", true, true}, {"--no-publish ", false, false}, {"--no-formal ", true, false},
		{"--publish ", true, true}, {"--formal ", true, true}, {"--publish --no-formal ", true, false},
	} {
		t.Run(tc.flags, func(t *testing.T) {
			h, lookup := remoteHarness(t)
			v2Command(h, "/review "+tc.flags+"pairtask/pairtask#295 @local-only")
			req := h.take(protocol.TypeOperatorReview)
			chatEqual(t, "local request operation", req.ReviewOp, protocol.ReviewOpRequest)
			chatEqual(t, "local reviewer identity", req.ReviewSpec.Reviewer, "local-only")
			policy := protocol.ReviewPolicy{Publish: true, Formal: true}
			if req.ReviewSpec.Policy != nil {
				policy = *req.ReviewSpec.Policy
			}
			chatEqual(t, "local publication compatibility", policy.Publish, tc.publish)
			chatEqual(t, "local formal compatibility", policy.Formal, tc.formal)
			chatEqual(t, "local request does not use remote lookup", len(lookup.calls), 0)
		})
	}
}

func TestReviewRemoteAcceptanceCompletionLengthensCollidingKeyPrefixes(t *testing.T) {
	first := contactChatRow("sha256:"+strings.Repeat("a", 64), "Office", true)
	second := contactChatRow("sha256:"+strings.Repeat("a", 12)+strings.Repeat("b", 52), "Office", true)
	h, _ := remoteHarness(t, first, second)
	for i := 0; i < 2; i++ {
		h.input(chatKey(tea.KeyTab, 0))
		req := h.take(protocol.TypeOperatorContact)
		page := contactMessagesPage(req.ContactKey)
		page.ContactPage.Directory = []string{"reviewer"}
		h.reply(req, page)
		h.await(func() bool { return h.m.State().InFlight == 0 })
	}
	commandCompleteLine(h, strings.TrimPrefix(remotePinnedCommand("@rev", 40), "/"))
	p := h.m.State().Picker
	if p == nil {
		t.Fatal("remote reviewer completion absent")
	}
	for _, token := range []string{"@reviewer@sha256:" + strings.Repeat("a", 13), "@reviewer@sha256:" + strings.Repeat("a", 12) + "b"} {
		if !slices.Contains(p.Items, token) {
			t.Fatalf("collision-safe completion %q absent from %v", token, p.Items)
		}
	}
	if slices.Contains(p.Items, "@reviewer@Office") {
		t.Fatal("ambiguous display alias offered as routing token")
	}
}

func TestReviewRemoteAcceptanceExplicitPinsBypassLookupAndKeepPolicy(t *testing.T) {
	for _, tc := range []struct {
		flags           string
		publish, formal bool
	}{
		{"", false, false}, {"--no-publish ", false, false}, {"--publish ", true, false}, {"--formal ", true, true}, {"--publish --no-formal ", true, false},
	} {
		t.Run(tc.flags, func(t *testing.T) {
			h, lookup := remoteHarness(t)
			pins := remotePins()
			v2Command(h, "/review "+tc.flags+"--pin "+pins.Base+".."+pins.Head+" pairtask/pairtask#295 @reviewer@Office focus")
			req := h.take(protocol.TypeOperatorContact)
			chatEqual(t, "explicit pins bypass code host", len(lookup.calls), 0)
			chatEqual(t, "publish choice", req.ReviewAsk.Publish, tc.publish)
			chatEqual(t, "formal choice", req.ReviewAsk.Formal, tc.formal)
			chatEqual(t, "override base", req.ReviewAsk.Base, pins.Base)
			chatEqual(t, "override head", req.ReviewAsk.Head, pins.Head)
			remoteSettle(h, req)
		})
	}
}

func TestReviewRemoteAcceptanceInvalidPinsAndConflictingFlagsNeverSend(t *testing.T) {
	for _, flags := range []string{
		"--pin main..HEAD", "--pin " + strings.Repeat("A", 40) + ".." + strings.Repeat("b", 40),
		"--publish --no-publish", "--formal --no-formal", "--formal --no-publish",
	} {
		t.Run(flags, func(t *testing.T) {
			h, lookup := remoteHarness(t)
			v2Command(h, "/review "+flags+" pairtask/pairtask#295 @reviewer@Office")
			chatEqual(t, "invalid input never invokes lookup", len(lookup.calls), 0)
			chatEqual(t, "invalid input never reaches broker", h.m.State().InFlight, 0)
			want := "conflict"
			if strings.HasPrefix(flags, "--pin") {
				want = "full commit IDs"
			}
			v3Contains(t, h.m.State().Status, want)
		})
	}
}

func TestReviewRemoteAcceptanceUnclosedQuoteNeverStartsLookupOrSends(t *testing.T) {
	h, lookup := remoteHarness(t)
	v2Command(h, remotePinnedCommand(`"@reviewer@Office`, 40))
	v3Contains(t, h.m.State().Status, "never closed")
	chatEqual(t, "unclosed quote never resolves", len(lookup.calls), 0)
	chatEqual(t, "unclosed quote never sends", h.m.State().InFlight, 0)
}
