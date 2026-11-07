package chat_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestRemoteAddressAcceptanceQualifiedSquareTagExplainsPairedTab(t *testing.T) {
	for _, qualifier := range []string{"Office", "key"} {
		h, key := remoteAddressSquare(t)
		if qualifier == "key" {
			qualifier = key
		}
		text := remoteAddressHeld(t, h, "@home-a@"+qualifier+" remote question")
		remoteAddressTabGuidance(t, text)
		v3Contains(t, text, "office")
	}
}

func TestRemoteAddressAcceptanceBareCollisionPreviewNamesLocalChoice(t *testing.T) {
	h, _ := remoteAddressSquare(t)
	v2Type(h, "@home-a local question")
	text := strings.ToLower(h.m.State().Hint)
	v3Contains(t, text, "home-a")
	v3Contains(t, text, "office")
	if !strings.Contains(text, "here") && !strings.Contains(text, "local") {
		t.Fatalf("same-name preview never identifies the local choice: %s", text)
	}
	remoteAddressTabGuidance(t, text)
	chatEqual(t, "preview sends nothing", h.m.State().InFlight, 0)
}

func TestRemoteAddressAcceptanceBareCollisionRetainsLocalPublicRouting(t *testing.T) {
	h, _ := remoteAddressSquare(t)
	v2Type(h, "@home-a local question")
	request := v2Send(h)
	chatEqual(t, "bare tag remains local operator send", request.Type, protocol.TypeOperatorSend)
	chatEqual(t, "Square assignment stays public", request.To, "*")
	if !slices.Equal(request.Mentions, []string{"home-a"}) {
		t.Fatalf("local assignment target changed: %q", request.Mentions)
	}
	chatEqual(t, "no remote contact selected", request.ContactKey, "")
}

func TestRemoteAddressAcceptanceLiteralToStillNamesLocalID(t *testing.T) {
	h, _ := remoteAddressSquare(t)
	v2Command(h, `/to ["home-a@Office"]`)
	v2Type(h, "literal local agent ID")
	request := v2Send(h)
	chatEqual(t, "explicit literal ID unchanged", request.To, "home-a@Office")
	chatEqual(t, "literal addressing never becomes remote", request.ContactKey, "")
}

func TestRemoteAddressAcceptanceUnavailableQualifierHasPreciseRefusal(t *testing.T) {
	for _, state := range []string{"unknown", "ambiguous", "confirm", "paused", "removed"} {
		t.Run(state, func(t *testing.T) {
			h, key := remoteAddressSquare(t)
			remoteAddressUnavailable(h, key, state)
			text := remoteAddressHeld(t, h, "@home-a@Office remote question")
			if state == "removed" {
				state = "unknown"
			}
			v3Contains(t, text, state)
		})
	}
}

func remoteAddressUnavailable(h *chatHarness, key, state string) {
	h.t.Helper()
	row := contactChatRow(key, "Office", state != "confirm")
	if state == "paused" {
		now := time.Now()
		row.PausedAt = &now
	}
	rows := []protocol.PairingContactView{row}
	switch state {
	case "unknown", "removed":
		rows = nil
	case "ambiguous":
		rows = append(rows, contactChatRow("sha256:"+strings.Repeat("c", 64), "Office", true))
	}
	remoteAddressContacts(h, rows...)
}

func TestRemoteAddressAcceptanceMixedPrefixNeverPartiallySends(t *testing.T) {
	h, _ := remoteAddressSquare(t)
	text := remoteAddressHeld(t, h, "@home-b @home-a@Office mixed question")
	remoteAddressTabGuidance(t, text)
}
