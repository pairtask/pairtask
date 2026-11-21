package chat_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// File references in a paired conversation (#419): each is shown as data,
// one line, with its label, size and the start of its digest; nothing in a
// label can draw on the terminal.

// fileRefsChatAny: text holds at least one of parts.
func fileRefsChatAny(text string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}

func fileRefsChatRow(t *testing.T, files string) *chatHarness {
	t.Helper()
	row := contactLayoutRow("with-files", 1)
	row.Direction, row.State = "in", "delivered"
	if err := json.Unmarshal([]byte(`{"files":`+files+`}`), &row); err != nil {
		t.Fatal(err)
	}
	return contactLayoutHarness(t, row)
}

func TestFileRefsChatAcceptanceShowsLabelSizeAndShortDigest(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	h := fileRefsChatRow(t, `[{"path":"build/report.pdf","size":1048576,"sha256":"`+digest+`"},{"path":"notes.txt","size":0,"sha256":"`+strings.Repeat("c", 64)+`"}]`)
	view := ansi.Strip(h.m.View().Content)
	for _, want := range []string{"build/report.pdf", "notes.txt", digest[:12], strings.Repeat("c", 12)} {
		v3Contains(t, view, want)
	}
	if !fileRefsChatAny(view, "1.0 MiB", "1 MiB", "1048576") {
		t.Fatalf("size of the first reference isn't shown: %q", view)
	}
	if strings.Contains(view, digest) {
		t.Fatal("a conversation row shows the whole digest; the short form is enough there")
	}
	chatEqual(t, "showing references emits no request", h.m.State().InFlight, 0)
}

func TestFileRefsChatAcceptanceLabelCannotDrawOnTheTerminal(t *testing.T) {
	h := fileRefsChatRow(t, `[{"path":"evil\u001b[2J\u0007name\r\nsecond","size":1,"sha256":"`+strings.Repeat("d", 64)+`"}]`)
	raw := h.m.View().Content
	if strings.Contains(raw, "\x1b[2J") || strings.ContainsAny(raw, "\x07\r") {
		t.Fatalf("a reference label reached the terminal unescaped: %q", raw)
	}
	v3Contains(t, ansi.Strip(raw), "evil")
}

func TestFileRefsChatAcceptanceRowWithoutReferencesIsUnchanged(t *testing.T) {
	plain := contactLayoutRow("plain", 1)
	plain.Direction, plain.State = "in", "delivered"
	without := ansi.Strip(contactLayoutHarness(t, plain).m.View().Content)
	// The header shows the contact's identity key, so only the message's own
	// lines are checked: the body, and nothing after it that sizes a file.
	_, after, found := strings.Cut(without, "BODY plain")
	if !found {
		t.Fatalf("fixture row not shown: %q", without)
	}
	message, _, _ := strings.Cut(after, "╭")
	if fileRefsChatAny(message, "MiB", " B ", "…") {
		t.Fatalf("a row without references shows one: %q", message)
	}
}
