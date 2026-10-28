package messaging_test

import (
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func TestMessageIDAcceptanceWireSafeBounds(t *testing.T) {
	for _, id := range []string{"message-one", "a", strings.Repeat("x", 128)} {
		if !messaging.ValidID(id) {
			t.Fatalf("valid ID rejected: %q", id)
		}
	}
	for _, id := range []string{"", strings.Repeat("x", 129), "two words", "line\nbreak", "tab\tid", "nul\x00id", "del\x7f", "café"} {
		if messaging.ValidID(id) {
			t.Fatalf("invalid ID accepted: %q", id)
		}
	}
}

func TestMessageTextAcceptanceCountsUTF8Bytes(t *testing.T) {
	for _, text := range []string{"hello", "hello\n世界", strings.Repeat("é", 8192)} {
		if !messaging.ValidText(text) {
			t.Fatalf("valid UTF-8 text rejected (%d bytes)", len(text))
		}
	}
	for _, text := range []string{"", string([]byte{0xff}), strings.Repeat("a", 16385), strings.Repeat("é", 8193)} {
		if messaging.ValidText(text) {
			t.Fatalf("invalid text accepted (%d bytes)", len(text))
		}
	}
}

func TestMessageRefusalAcceptanceClassCannotChange(t *testing.T) {
	for _, reason := range []string{"invalid", "too_large", "conflict", "too_old"} {
		refusal := messaging.Refuse(reason)
		if !refusal.Valid() || refusal.Class != messaging.Terminal {
			t.Fatalf("wrong refusal: %+v", refusal)
		}
		refusal.Class = messaging.Transient
		if refusal.Valid() {
			t.Fatalf("terminal reason accepted as transient: %s", reason)
		}
	}
	for _, reason := range []string{"inbox_full", "busy"} {
		refusal := messaging.Refuse(reason)
		if !refusal.Valid() || refusal.Class != messaging.Transient {
			t.Fatalf("wrong refusal: %+v", refusal)
		}
		refusal.Class = messaging.Terminal
		if refusal.Valid() {
			t.Fatalf("transient reason accepted as terminal: %s", reason)
		}
	}
	if messaging.Refuse("unknown").Valid() {
		t.Fatal("unknown reason accepted")
	}
}
