package chat_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPairedTabPickerAcceptanceBareAtOffersBothSidesAndOperators(t *testing.T) {
	h, _ := pairedTabHarness(t)
	v2Type(h, "@")
	p := h.m.State().Picker
	if p == nil {
		t.Fatal("bare @ did not offer recipients")
	}
	for _, forms := range [][]string{{"home-a@here"}, {"home-b@here"}, {"operator"}, {"home-a@Office", "home-a"}, {"remote-a@Office", "remote-a"}, {"operator@Office"}} {
		if pairedTabChoice(p.Items, forms...) < 0 {
			t.Errorf("bare @ omits %q: %q", forms, p.Items)
		}
	}
	chatEqual(t, "bare @ keeps draft", h.m.State().Draft, "@")
	chatEqual(t, "opening choices sends nothing", h.m.State().InFlight, 0)
}
func TestPairedTabPickerAcceptanceLabelsAndInsertedLocalTokenStayLocal(t *testing.T) {
	for _, token := range []string{"home-a@here", "home-b@here"} {
		t.Run(token, func(t *testing.T) {
			h, _ := pairedTabHarness(t)
			v2Type(h, "@home")
			if strings.Contains(pairedTabView(h), "@here@Office") {
				t.Error("picker renders an invalid doubled workspace suffix")
			}
			pairedTabPick(t, h, token)
			chatEqual(t, "inserted local token", h.m.State().Draft, "@"+token+" ")
			v2Type(h, "question")
			h.input(chatKey(tea.KeyEnter, 0))
			req := h.take(protocol.TypeOperatorContact)
			here, operator := pairedMentionsChatFields(t, req)
			chatEqual(t, "one local recipient", len(here), 1)
			chatEqual(t, "canonical local identity", here[0], strings.TrimSuffix(token, "@here"))
			chatEqual(t, "local agent choice is not operator", operator, false)
			chatEqual(t, "local-only tag preserves remote operator default", len(req.ContactTo), 0)
			pairedMentionsChatAccept(t, h, req)
		})
	}
}
func TestPairedTabPickerAcceptanceOperatorsHaveDistinctInsertedTokens(t *testing.T) {
	for _, token := range []string{"operator", "operator@Office"} {
		t.Run(token, func(t *testing.T) {
			h, _ := pairedTabHarness(t)
			v2Type(h, "@op")
			pairedTabPick(t, h, token)
			chatEqual(t, "operator choice inserts its own scope", h.m.State().Draft, "@"+token+" ")
			v2Type(h, "attention")
			h.input(chatKey(tea.KeyEnter, 0))
			req := h.take(protocol.TypeOperatorContact)
			here, operator := pairedMentionsChatFields(t, req)
			chatEqual(t, "operator choice creates no agent delivery", len(here), 0)
			chatEqual(t, "only bare operator means local operator", operator, token == "operator")
			chatEqual(t, "operator choice keeps peer operator audience", len(req.ContactTo), 0)
			pairedMentionsChatAccept(t, h, req)
		})
	}
}
func TestPairedTabPickerAcceptanceOperatorAliasDeduplicatesResolvedIdentity(t *testing.T) {
	h, _ := pairedTabHarness(t)
	v2Type(h, "@operator @operator@here @home-a@here @home-a@here question")
	h.input(chatKey(tea.KeyEnter, 0))
	req := h.take(protocol.TypeOperatorContact)
	here, operator := pairedMentionsChatFields(t, req)
	chatEqual(t, "local operator aliases make one local copy", operator, true)
	chatEqual(t, "local aliases make one agent copy", len(here), 1)
	chatEqual(t, "same logical agent retained", here[0], "home-a")
	chatEqual(t, "default remote operator retained", len(req.ContactTo), 0)
	pairedMentionsChatAccept(t, h, req)
}

func TestPairedTabPickerAcceptanceSidesAreContiguousAndClearlyRendered(t *testing.T) {
	h, _ := pairedTabHarness(t)
	v2Type(h, "@")
	p := h.m.State().Picker
	if p == nil {
		t.Fatal("bare @ did not offer grouped recipients")
	}
	groups := []struct {
		name  string
		forms [][]string
	}{
		{"local", [][]string{{"home-a@here"}, {"home-b@here"}, {"operator"}}},
		{"peer", [][]string{{"home-a@Office", "home-a"}, {"remote-a@Office", "remote-a"}, {"operator@Office"}}},
	}
	bounds := make([][2]int, 0, 2)
	for _, group := range groups {
		first, last := pairedTabGroupBounds(t, p.Items, group.forms)
		bounds = append(bounds, [2]int{first, last})
	}
	if !(bounds[0][1] < bounds[1][0] || bounds[1][1] < bounds[0][0]) {
		t.Fatalf("local and peer choices are interleaved: %q", p.Items)
	}
	view := pairedTabView(h)
	for _, token := range []string{"@home-a@here", "@remote-a@Office", "@operator@Office"} {
		v3Contains(t, view, token)
	}
	if strings.Contains(view, "@here@Office") {
		t.Fatal("grouping labels turn a local tag into an invalid remote tag")
	}
}

func pairedTabGroupBounds(t *testing.T, items []string, forms [][]string) (int, int) {
	t.Helper()
	first, last := len(items), -1
	for _, choices := range forms {
		index := pairedTabChoice(items, choices...)
		if index < 0 {
			t.Fatalf("recipient group lacks %q: %q", choices, items)
		}
		first = min(first, index)
		last = max(last, index)
	}
	return first, last
}
