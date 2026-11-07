package chat_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func staleChatHarness(t *testing.T, supported bool) *chatHarness {
	t.Helper()
	s := newChatScript()
	h := &chatHarness{t: t, wire: s, results: make(chan tea.Msg, 128), done: make(chan struct{}), dials: make(chan chatDialResult, 8)}
	caps := slices.DeleteFunc(slices.Clone(protocol.SessionCapabilities), func(c string) bool { return c == "review_supersede" })
	if supported {
		caps = append(caps, "review_supersede")
	}
	h.dials <- chatDialResult{wire: s, session: protocol.SessionInfo{SessionID: "owner", Epoch: "epoch", Capabilities: caps, OpenedAt: time.Now()}}
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
	h.input(tea.WindowSizeMsg{Width: 160, Height: 40})
	contactChatBaseline(h, contactChatRow("sha256:"+strings.Repeat("a", 64), "Office", true))
	return h
}

func TestStaleAskChatAcceptanceExplicitReplacementUsesOperatorAsk(t *testing.T) {
	h := staleChatHarness(t, true)
	command := "/review --replaces prior --pin " + strings.Repeat("1", 40) + ".." + strings.Repeat("3", 40) + " pairtask/pairtask#259 @reviewer@Office"
	v2Command(h, command)
	request := h.take(protocol.TypeOperatorContact)
	chatEqual(t, "operator paired ask", request.ContactOp, protocol.ContactOpReviewAsk)
	data, err := json.Marshal(request.ReviewAsk)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	chatEqual(t, "explicit parent carried", fields["supersedes"], any("prior"))
	chatEqual(t, "explicit new head", request.ReviewAsk.Head, strings.Repeat("3", 40))
	remoteSettle(h, request)
}

func TestStaleAskChatAcceptanceOldBrokerRefusesAndKeepsDraft(t *testing.T) {
	h := staleChatHarness(t, false)
	draft := "/review --replaces prior pairtask/pairtask#259 @reviewer@Office"
	v2Type(h, draft)
	h.input(chatKey(tea.KeyEnter, 0))
	chatEqual(t, "unsupported replacement sends nothing", h.m.State().InFlight, 0)
	chatEqual(t, "input preserved", h.m.State().Draft, draft)
	v3Contains(t, strings.ToLower(h.m.State().Status+" "+h.m.State().Hint), "upgrade")
}
