package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

// The 3000-entry case deliberately exceeds the CLI's default 1024-entry bound.
// Entries are representative mixed markdown, not the operator's private text.
func performanceModel(tb testing.TB, count int) *Model {
	tb.Helper()
	m := New(Options{Bounds: Bounds{Entries: count + 10}, Now: func() time.Time { return time.Unix(1700000000, 0) }})
	tb.Cleanup(m.cancel)
	m.width, m.height = 100, 36
	for n := 1; n <= count; n++ {
		body := fmt.Sprintf("Message %d: **review result** with `code` and ordinary prose.\n\n- First check: preserve the reading position across arrivals.\n- Second check: display updated delivery and claim state.\n\n| Check | Result |\n| --- | --- |\n| Transport | Passed |\n| Identity | Passed |", n)
		payload, err := json.Marshal(map[string]string{"text": body})
		if err != nil {
			tb.Fatal(err)
		}
		msg := protocol.Message{MessageID: fmt.Sprintf("perf-%05d", n), Seq: uint64(n), From: "agent", To: "operator", Payload: payload}
		m.timeline.insert(newEntry(msg, "received", false))
	}
	m.view.following = false
	m.view.anchorID = fmt.Sprintf("perf-%05d", count/2)
	return m
}

func BenchmarkChatHistoryInteraction(b *testing.B) {
	for _, count := range []int{100, 1000, 3000} {
		b.Run(fmt.Sprintf("entries-%d", count), func(b *testing.B) {
			b.Run("redraw", func(b *testing.B) { benchmarkHistoryAction(b, count, false) })
			b.Run("wheel-and-redraw", func(b *testing.B) { benchmarkHistoryAction(b, count, true) })
		})
	}
}

func benchmarkHistoryAction(b *testing.B, count int, wheel bool) {
	m := performanceModel(b, count)
	_ = m.View()
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if wheel {
			performanceWheel(m, n)
		}
		if content := m.View().Content; !strings.Contains(content, "Message") {
			b.Fatal("benchmark lost transcript")
		}
	}
}

func performanceWheel(m *Model, n int) {
	button := tea.MouseWheelUp
	if n%2 == 1 {
		button = tea.MouseWheelDown
	}
	m.Update(tea.MouseWheelMsg{Button: button, Y: 5})
}
