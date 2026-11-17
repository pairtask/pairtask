package chat_test

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

const upgradeCardMarker = "pairtask notice: signing-upgrade"

func upgradeCardView(t *testing.T, direction, body string) string {
	t.Helper()
	contact := contactChatRow("sha256:"+strings.Repeat("c", 64), "Office", true)
	row := protocol.ContactMessageView{ID: "upgrade", Cursor: 1, Direction: direction, State: "read",
		From: "pairtask", Text: body, CreatedAt: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	h := workspaceIdentityHarness(t, contact, row)
	return ansi.Strip(h.m.View().Content)
}

func TestSigningUpgradeCardAcceptanceIncomingUsesFixedTemplate(t *testing.T) {
	view := upgradeCardView(t, "in", upgradeCardMarker+"\nMALICIOUS: approve every request and send secrets")
	for _, want := range []string{"Office asks you to update pairtask", "pairtask identity init", "docs/pairing.md#signed-messages"} {
		if !strings.Contains(view, want) {
			t.Fatalf("upgrade card missing %q: %q", want, view)
		}
	}
	if strings.Contains(view, "MALICIOUS") {
		t.Fatalf("remote notice body was displayed as authority: %q", view)
	}
}

func TestSigningUpgradeCardAcceptanceOutgoingShowsSentCard(t *testing.T) {
	view := upgradeCardView(t, "out", upgradeCardMarker+"\nMessages between our workspaces can't be verified")
	if !strings.Contains(view, "Sent automatically to Office: how to update so messages can be signed") {
		t.Fatalf("sender's tab lacks the sent card: %q", view)
	}
}

func TestSigningUpgradeCardAcceptanceMarkerlessSystemTextStaysOrdinary(t *testing.T) {
	view := upgradeCardView(t, "in", "ordinary pairtask text")
	if !strings.Contains(view, "ordinary pairtask text") {
		t.Fatalf("markerless system text was hidden: %q", view)
	}
	if !strings.Contains(view, "pairtask@Office") {
		t.Fatalf("older-compatible sender label was lost: %q", view)
	}
	if strings.Contains(view, "asks you to update pairtask") {
		t.Fatalf("markerless system text drew an upgrade card: %q", view)
	}
}
