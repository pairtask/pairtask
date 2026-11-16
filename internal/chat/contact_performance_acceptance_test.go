package chat

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func largeContactModel(tb testing.TB, count int) (*Model, string) {
	tb.Helper()
	m := New(Options{})
	tb.Cleanup(m.cancel)
	m.width, m.height, m.conn, m.baseline = 100, 36, Connected, true
	m.session.Capabilities = protocol.SessionCapabilities
	contact := "sha256:" + strings.Repeat("a", 64)
	m.pairing.snapshot = &protocol.PairingSnapshot{Contacts: []protocol.PairingContactView{{
		IdentityKey: contact, Name: "Work Mac", Confirmed: true,
	}}}
	m.conv.selected = contactKey(contact)
	thread := m.contacts.thread(contact)
	thread.loaded = true
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	for n := 1; n <= count; n++ {
		id := fmt.Sprintf("paired-%05d", n)
		row := protocol.ContactMessageView{ID: id, Contact: contact, Cursor: int64(n), Direction: "in",
			State: "read", CreatedAt: at, From: "remote-agent",
			Text: fmt.Sprintf("Paired message %d: **review result** with `code`.\n\n- First check: keep the scroll position.\n- Second check: render updated state.\n\n| Check | Result |\n| --- | --- |\n| Transport | Passed |\n| Identity | Passed |", n)}
		if n > 1 {
			row.ReplyID = fmt.Sprintf("paired-%05d", n-1)
			row.ReplySide = protocol.ReplyBySender
		}
		thread.merge([]protocol.ContactMessageView{row})
	}
	return m, contact
}

func TestContactPerformanceAcceptanceWarmScrollAllocationBudget(t *testing.T) {
	m, _ := largeContactModel(t, 1000)
	_ = m.View()
	allocations := testing.AllocsPerRun(3, func() {
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, Y: 10})
		_ = m.View()
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, Y: 10})
		_ = m.View()
	})
	if allocations > 100000 {
		t.Fatalf("warm paired-tab wheel and redraw allocates %.0f objects; budget 100000", allocations)
	}
}

func TestContactPerformanceAcceptanceChangedBodyAndStatusRemainVisible(t *testing.T) {
	m, contact := largeContactModel(t, 1)
	before := ansi.Strip(m.View().Content)
	if !strings.Contains(before, "Paired message 1") {
		t.Fatal("first contact body was not shown")
	}
	row := m.contacts.thread(contact).rows[rowRef{"in", 0, "paired-00001"}]
	row.Text, row.AckState = "Updated paired body with a visible tail", protocol.AckDone
	m.contacts.thread(contact).merge([]protocol.ContactMessageView{row})
	after := ansi.Strip(m.View().Content)
	if !strings.Contains(after, "Updated paired body") {
		t.Fatalf("refreshed message lost its new body:\n%s", after)
	}
	if !strings.Contains(after, AckDone) {
		t.Fatalf("refreshed message lost its acknowledgment:\n%s", after)
	}
	if strings.Contains(after, "Paired message 1") {
		t.Fatalf("refreshed message kept its old body:\n%s", after)
	}
	m.Update(tea.WindowSizeMsg{Width: 45, Height: 36})
	resized := ansi.Strip(m.View().Content)
	if !strings.Contains(resized, "visible tail") {
		t.Fatalf("resized message clipped its tail:\n%s", resized)
	}
	for _, line := range strings.Split(resized, "\n") {
		if ansi.StringWidth(line) > 45 {
			t.Fatalf("resized paired tab has a wide row: %q", line)
		}
	}
}

func TestContactPerformanceAcceptanceTrustRefreshChangesCachedVerdict(t *testing.T) {
	m, contact := largeContactModel(t, 0)
	row, roots := contactSigningFixture(t)
	thread := m.contacts.thread(contact)
	thread.signing = roots
	thread.merge([]protocol.ContactMessageView{row})
	trusted := strings.Join(m.contactLines(contact).lines, "\n")
	if !strings.Contains(trusted, "signed by their operator") {
		t.Fatalf("accepted key did not verify: %s", trusted)
	}
	thread.signing.roots[0].Approved = false
	thread.merge(nil) // a new page can change roots without adding messages
	untrusted := strings.Join(m.contactLines(contact).lines, "\n")
	if !strings.Contains(untrusted, "key not accepted") || strings.Contains(untrusted, "signed by their operator") {
		t.Fatalf("key change kept the cached trust verdict: %s", untrusted)
	}
}

func BenchmarkContactHistoryInteraction(b *testing.B) {
	for _, count := range []int{100, 1000} {
		b.Run(fmt.Sprintf("messages-%d", count), func(b *testing.B) {
			m, _ := largeContactModel(b, count)
			_ = m.View()
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, Y: 10})
				_ = m.View()
				m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, Y: 10})
				_ = m.View()
			}
		})
	}
}
