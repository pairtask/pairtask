package chat_test

import (
	"slices"
	"testing"
)

func TestPairedTabPickerAcceptanceWhitespaceFinishesScopedTags(t *testing.T) {
	for _, delimiter := range []struct{ name, text, inserted string }{
		{"tab", "\t", "    "}, // the editor expands inserted tabs to four spaces
		{"em space", "\u2003", "\u2003"}, {"ideographic space", "\u3000", "\u3000"},
	} {
		t.Run(delimiter.name, func(t *testing.T) {
			for _, token := range []string{"home-a@Office", "home-a@here"} {
				t.Run(token, func(t *testing.T) {
					h, _ := pairedTabHarness(t)
					draft := "@" + token + delimiter.text
					v2Type(h, draft)
					p := h.m.State().Picker
					if p != nil && slices.Contains(p.Items, token) {
						t.Errorf("completed scoped identity remains offered after %s: %q", delimiter.name, p.Items)
					}
					chatEqual(t, "whitespace keeps editor-normalized draft", h.m.State().Draft, "@"+token+delimiter.inserted)
					chatEqual(t, "whitespace only commits preview", h.m.State().InFlight, 0)
				})
			}
		})
	}
}
