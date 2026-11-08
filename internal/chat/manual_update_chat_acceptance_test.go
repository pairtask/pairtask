package chat_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func manualUpdateNotice(state, action string) map[string]any {
	now := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	return map[string]any{"state": state, "action": action, "app_release": "workspace-v0.0.0-build.99.1",
		"app_commit": strings.Repeat("a", 40), "host_release": "workspace-v0.0.0-build.100.1",
		"available_release": "workspace-v0.0.0-build.100.1", "checked_at": now, "observed_at": now}
}

// Decode through today's public type so missing support fails semantically.
func manualUpdateSnapshot(t *testing.T, revision uint64, notice map[string]any) protocol.Message {
	t.Helper()
	data, err := json.Marshal(map[string]any{"watermark": map[string]any{"epoch": "epoch", "revision": revision}, "update_notice": notice})
	if err != nil {
		t.Fatal(err)
	}
	var snapshot protocol.OperatorSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	return protocol.Message{Type: protocol.TypeOperatorSnapshot, Snapshot: &snapshot}
}

func manualUpdateHarness(t *testing.T, supported bool, notice map[string]any) *chatHarness {
	t.Helper()
	s := newChatScript()
	h := &chatHarness{t: t, wire: s, results: make(chan tea.Msg, 128), done: make(chan struct{}), dials: make(chan chatDialResult, 8)}
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilities), func(cap string) bool { return cap == "update_notice" })
	if supported {
		caps = append(caps, "update_notice")
	}
	h.dials <- chatDialResult{wire: s, session: protocol.SessionInfo{SessionID: "owner", Epoch: "epoch", Capabilities: caps}}
	h.m = chat.New(chat.Options{Dial: func(context.Context) (chat.Transport, protocol.SessionInfo, error) {
		select {
		case result := <-h.dials:
			return result.wire, result.session, result.err
		case <-h.done:
			return nil, protocol.SessionInfo{}, context.Canceled
		}
	}})
	t.Cleanup(func() { close(h.done); s.Close() })
	h.launch(h.m.Init())
	h.input(tea.WindowSizeMsg{Width: 180, Height: 32})
	h.reply(h.take(protocol.TypeOperatorSnapshot), manualUpdateSnapshot(t, 1, notice))
	inbox, history := h.take(protocol.TypeOperatorReceive), h.take(protocol.TypeOperatorHistory)
	h.reply(inbox, chatInbox())
	h.reply(history, chatHistory())
	h.await(func() bool { return h.m.State().InFlight == 0 })
	return h
}

func TestManualUpdateChatAcceptanceFooterAndVersionAreInformational(t *testing.T) {
	h := manualUpdateHarness(t, true, manualUpdateNotice("update_available", "upgrade_app"))
	frame := ansi.Strip(h.m.View().Content)
	for _, word := range []string{"100.1", "99.1", "pk upgrade"} {
		v3Contains(t, frame, word)
	}
	v2Command(h, "/version")
	h.await(func() bool { return h.m.State().InFlight == 0 })
	frame = ansi.Strip(h.m.View().Content)
	for _, word := range []string{"100.1", "99.1", "checked", "pk upgrade"} {
		v3Contains(t, frame, word)
	}
	chatEqual(t, "display never sends an operator message", h.m.State().Unread, 0)
	for _, req := range h.requests {
		if req.Type == protocol.TypeOperatorSend || req.Type == "operator_update_notice" {
			t.Fatalf("chat display caused a write: %+v", req)
		}
	}
}

func TestManualUpdateChatAcceptanceEventClearsNoticeAndPreservesDraft(t *testing.T) {
	h := manualUpdateHarness(t, true, manualUpdateNotice("update_available", "upgrade_app"))
	v2Type(h, "my unsent draft")
	before := h.m.State()
	h.wire.in <- protocol.Message{Type: protocol.TypeEvent, Event: &protocol.SessionEvent{
		Kind: "update_notice_changed", Watermark: protocol.Watermark{Epoch: "epoch", Revision: 2}}}
	req := h.take(protocol.TypeOperatorSnapshot)
	notice := manualUpdateNotice("current", "none")
	notice["app_release"] = notice["available_release"]
	h.reply(req, manualUpdateSnapshot(t, 2, notice))
	h.await(func() bool { return h.m.State().InFlight == 0 })
	chatEqual(t, "draft retained", h.m.State().Draft, before.Draft)
	chatEqual(t, "audience retained", h.m.State().Selected, before.Selected)
	if strings.Contains(ansi.Strip(h.m.View().Content), "host: pk upgrade") {
		t.Fatal("observed current app retained an outdated update reminder")
	}
}

func TestManualUpdateChatAcceptanceOldBrokerAndMissingNoticeAreExplicit(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(strings.ToLower(map[bool]string{false: "old", true: "unreported"}[supported]), func(t *testing.T) {
			h := manualUpdateHarness(t, supported, nil)
			v2Command(h, "/version")
			want := "this broker does not report versions"
			if supported {
				want = "not reported yet"
			}
			v3Contains(t, ansi.Strip(h.m.View().Content), want)
			chatEqual(t, "local version display has no request", h.m.State().InFlight, 0)
		})
	}
}

func TestManualUpdateChatAcceptanceHostAdviceAndSmallFrames(t *testing.T) {
	h := manualUpdateHarness(t, true, manualUpdateNotice("update_available", "update_host"))
	v3Contains(t, ansi.Strip(h.m.View().Content), "rerun the installer")
	for _, size := range [][2]int{{80, 24}, {40, 12}, {20, 6}} {
		h.input(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v2FrameFits(t, h, size[0], size[1])
	}
}

func TestManualUpdateChatAcceptanceStaleMetadataIsLabelled(t *testing.T) {
	notice := manualUpdateNotice("update_available", "upgrade_app")
	notice["available_state"] = "stale"
	h := manualUpdateHarness(t, true, notice)
	v2Command(h, "/version")
	v3Contains(t, strings.ToLower(ansi.Strip(h.m.View().Content)), "stale")
}
