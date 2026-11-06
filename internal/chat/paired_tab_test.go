package chat

import (
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestTagAtCursorIsOnlyAnUnfinishedLeadingTag(t *testing.T) {
	for _, tc := range []struct {
		line, want string
		col        int
		ok         bool
	}{
		{"@rem", "@rem", 4, true},
		{"@remote-a body", "@remote-a", 3, true},
		{"@remote-a body", "", 0, false},
		{"@remote-a ", "", 10, false},
		{"@a @b", "@b", 5, true},
		{"hello @b", "", 8, false},
		{`\@b`, "", 3, false},
	} {
		got, ok := tagAtCursor([]rune(tc.line), tc.col)
		if got != tc.want || ok != tc.ok {
			t.Errorf("tagAtCursor(%q, %d) = %q, %v; want %q, %v", tc.line, tc.col, got, ok, tc.want, tc.ok)
		}
	}
}

func TestStoredHomeNamesOnlyASentRowsStoredRoute(t *testing.T) {
	for _, tc := range []struct {
		view protocol.ContactMessageView
		want string
	}{
		{protocol.ContactMessageView{Direction: "out", Here: []string{"home-a"}, OperatorHere: true}, " · home-a@here, operator@here"},
		{protocol.ContactMessageView{Direction: "out"}, ""},
		{protocol.ContactMessageView{Direction: "in", Here: []string{"home-a"}}, ""},
	} {
		if got := storedHome(tc.view); got != tc.want {
			t.Errorf("storedHome(%+v) = %q, want %q", tc.view, got, tc.want)
		}
	}
}

func TestEndsInSpaceUsesUnicodeWhitespace(t *testing.T) {
	cases := map[string]bool{"": false, "@a": false, "@a ": true, "@a\t": true, "@a ": true, "@a ": true}
	for line, want := range cases {
		if got := endsInSpace([]rune(line)); got != want {
			t.Errorf("endsInSpace(%q) = %v, want %v", line, got, want)
		}
	}
}
