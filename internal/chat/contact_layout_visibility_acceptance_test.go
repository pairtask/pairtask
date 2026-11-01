package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/pairtask/pairtask/internal/protocol"
)

func contactVisibilityModel(t *testing.T) (*Model, string) {
	t.Helper()
	m := New(Options{})
	t.Cleanup(m.cancel)
	m.width, m.conn, m.baseline = 200, Connected, true
	m.session.Capabilities = protocol.SessionCapabilities
	key := "sha256:" + strings.Repeat("a", 64)
	m.pairing.snapshot = &protocol.PairingSnapshot{Contacts: []protocol.PairingContactView{{IdentityKey: key, Name: "Peer", Confirmed: true}}}
	at := time.Date(2026, 11, 1, 12, 34, 0, 0, time.UTC)
	older := protocol.ContactMessageView{ID: "older", Cursor: 1, Direction: "in", State: "read", Text: "OLDER BODY", CreatedAt: at}
	unread := protocol.ContactMessageView{ID: "unread", Cursor: 2, Direction: "in", State: "received", From: "unread-agent", Text: "UNREAD BODY", CreatedAt: at,
		ReplyID: "missing-parent", ReplySide: protocol.ReplyBySender, ReviewState: "awaiting",
		ReviewAsk: &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "o/r", Change: "12"}, Base: "base", Head: "head"}}
	thread := m.contacts.thread(key)
	thread.loaded = true
	thread.merge([]protocol.ContactMessageView{older, unread})
	l := m.contactLines(key)
	for height := 6; height <= 40; height++ {
		m.height = height
		if m.layout().transcript == len(l.header)+1 {
			return m, key
		}
	}
	t.Fatal("fixture could not allocate exactly one message line")
	return nil, ""
}

func contactVisibilityLine(t *testing.T, m *Model, key, text string) int {
	t.Helper()
	for i, line := range m.contactLines(key).lines {
		if strings.Contains(ansi.Strip(line), text) {
			return i
		}
	}
	t.Fatalf("fixture line %q not found", text)
	return -1
}

func showContactVisibilityLine(m *Model, key string, line int) {
	l := m.contactLines(key)
	m.contacts.thread(key).offset = len(l.lines) - line - 1
}

func TestContactLayoutVisibilityAcceptanceSeparatorAloneNeverMarksRead(t *testing.T) {
	m, key := contactVisibilityModel(t)
	header := contactVisibilityLine(t, m, key, "unread-agent@Peer")
	if header <= 0 {
		t.Fatal("unread header has no preceding separator")
	}
	l := m.contactLines(key)
	if strings.TrimSpace(ansi.Strip(l.lines[header-1])) != "" {
		t.Fatal("messages have no blank separator before unread header")
	}
	showContactVisibilityLine(m, key, header-1)
	_, start, end := m.contactWindow(key, m.layout().transcript)
	if end-start != 1 {
		t.Fatalf("fixture shows %d message lines, want 1", end-start)
	}
	if ids := m.shownUnread(key); len(ids) != 0 {
		t.Fatalf("blank separator marked unread message: %v", ids)
	}
	if m.readShown(key) != nil {
		t.Fatal("blank separator generated a read request")
	}
}

func TestContactLayoutVisibilityAcceptanceMeaningfulLinesMarkReadOnce(t *testing.T) {
	for _, text := range []string{"unread-agent@Peer", "↳ re an earlier message", "UNREAD BODY", "review o/r#12"} {
		t.Run(text, func(t *testing.T) {
			m, key := contactVisibilityModel(t)
			showContactVisibilityLine(m, key, contactVisibilityLine(t, m, key, text))
			ids := m.shownUnread(key)
			if len(ids) != 1 {
				t.Fatalf("meaningful message line should mark one message: %v", ids)
			}
			if ids[0] != "unread" {
				t.Fatalf("wrong visible message: %v", ids)
			}
			m.readShown(key)
			if len(m.contacts.reads) != 1 {
				t.Fatal("visible message did not generate one read operation")
			}
			m.readShown(key)
			if len(m.contacts.reads) != 1 {
				t.Fatal("pending read generated a duplicate read operation")
			}
		})
	}
}
