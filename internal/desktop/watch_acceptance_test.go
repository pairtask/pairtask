package desktop

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type noticeFixture struct {
	contacts []protocol.PairingContactView
	counts   map[string]int
	failed   map[string]bool
	notices  []string
}

func (f *noticeFixture) call(_ context.Context, req protocol.Message) protocol.Message {
	if req.Type == protocol.TypeOperatorPairing {
		return protocol.Message{Type: protocol.TypeOperatorPairingResult,
			PairingResult: &protocol.PairingSnapshot{Contacts: f.contacts}}
	}
	if req.Type == protocol.TypeOperatorContact && req.ContactOp == protocol.ContactOpList {
		if f.failed[req.ContactKey] {
			return protocol.Message{Type: protocol.TypeError}
		}
		var messages []protocol.ContactMessageView
		for range f.counts[req.ContactKey] {
			messages = append(messages, protocol.ContactMessageView{Direction: "in", Text: "PRIVATE_MESSAGE_BODY"})
		}
		return protocol.Message{Type: protocol.TypeOperatorContactResult,
			ContactPage: &protocol.ContactMessagePage{Messages: messages}}
	}
	return protocol.Message{Type: protocol.TypeError}
}

func TestDesktopWatcherAcceptanceUnreadablePageKeepsOldMessagesQuiet(t *testing.T) {
	for _, tc := range []struct {
		name           string
		failAtBaseline bool
	}{
		{name: "after baseline"},
		{name: "at baseline", failAtBaseline: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &noticeFixture{
				contacts: []protocol.PairingContactView{{IdentityKey: "alpha-key", LocalName: "Alpha", Confirmed: true}},
				counts:   map[string]int{"alpha-key": 1},
				failed:   map[string]bool{"alpha-key": tc.failAtBaseline},
			}
			w := newWatcher(f.call, f.notifier, slog.New(slog.DiscardHandler))
			w.checkAll(context.Background())
			if !tc.failAtBaseline {
				f.failed["alpha-key"] = true
				w.check(context.Background(), "alpha-key")
			}
			delete(f.failed, "alpha-key")
			w.check(context.Background(), "alpha-key")
			assertWatcherNotices(t, f.notices)
			f.counts["alpha-key"] = 2
			w.check(context.Background(), "alpha-key")
			assertWatcherNotices(t, f.notices, "2 unread from Alpha")
		})
	}
}

func TestDesktopWatcherAcceptanceNewContactAnnouncesFirstMessage(t *testing.T) {
	f := &noticeFixture{
		contacts: []protocol.PairingContactView{{IdentityKey: "alpha-key", LocalName: "Alpha", Confirmed: true}},
		counts:   map[string]int{},
	}
	w := newWatcher(f.call, f.notifier, slog.New(slog.DiscardHandler))
	w.checkAll(context.Background())
	f.contacts = append(f.contacts, protocol.PairingContactView{IdentityKey: "beta-key", LocalName: "Beta", Confirmed: true})
	f.counts["beta-key"] = 1
	w.checkAll(context.Background())
	assertWatcherNotices(t, f.notices, "1 unread from Beta")
}

func (f *noticeFixture) notifier(_ context.Context, title, text string) error {
	if title != "pairtask" {
		f.notices = append(f.notices, "wrong title: "+title)
	} else {
		f.notices = append(f.notices, text)
	}
	return nil
}

func TestDesktopWatcherAcceptanceBaselinePrivacyAndPerContactThrottle(t *testing.T) {
	f := &noticeFixture{
		contacts: []protocol.PairingContactView{
			{IdentityKey: "alpha-key", LocalName: "Alpha", Confirmed: true},
			{IdentityKey: "beta-key", LocalName: "Beta", Confirmed: true},
		},
		counts: map[string]int{"alpha-key": 1},
	}
	w := newWatcher(f.call, f.notifier, slog.New(slog.DiscardHandler))
	now := time.Date(2026, time.November, 13, 14, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	w.checkAll(context.Background())
	if len(f.notices) != 0 {
		t.Fatalf("baseline sent notifications: %q", f.notices)
	}

	f.counts["alpha-key"], f.counts["beta-key"] = 2, 1
	w.check(context.Background(), "alpha-key")
	w.check(context.Background(), "beta-key")
	assertWatcherNotices(t, f.notices, "1 unread from Beta", "2 unread from Alpha")
	w.check(context.Background(), "alpha-key")
	assertWatcherNotices(t, f.notices, "1 unread from Beta", "2 unread from Alpha")

	now = now.Add(10 * time.Second)
	f.counts["alpha-key"], f.counts["beta-key"] = 3, 2
	w.check(context.Background(), "alpha-key")
	w.check(context.Background(), "beta-key")
	assertWatcherNotices(t, f.notices, "1 unread from Beta", "2 unread from Alpha")
	now = now.Add(21 * time.Second)
	w.release(context.Background())
	assertWatcherNotices(t, f.notices,
		"1 unread from Beta", "2 unread from Alpha", "2 unread from Beta", "3 unread from Alpha")
}

func TestDesktopWatcherAcceptanceReadFallAllowsLaterRise(t *testing.T) {
	f := &noticeFixture{
		contacts: []protocol.PairingContactView{{IdentityKey: "alpha-key", LocalName: "Alpha", Confirmed: true}},
		counts:   map[string]int{},
	}
	w := newWatcher(f.call, f.notifier, slog.New(slog.DiscardHandler))
	now := time.Date(2026, time.November, 13, 14, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	w.checkAll(context.Background())
	f.counts["alpha-key"] = 1
	w.check(context.Background(), "alpha-key")
	f.counts["alpha-key"] = 0
	w.check(context.Background(), "alpha-key")
	now = now.Add(10 * time.Second)
	f.counts["alpha-key"] = 1
	w.check(context.Background(), "alpha-key")
	assertWatcherNotices(t, f.notices, "1 unread from Alpha")
	now = now.Add(21 * time.Second)
	w.release(context.Background())
	assertWatcherNotices(t, f.notices, "1 unread from Alpha", "1 unread from Alpha")
}

func assertWatcherNotices(t *testing.T, got []string, want ...string) {
	t.Helper()
	for _, text := range got {
		if strings.Contains(text, "PRIVATE_MESSAGE_BODY") {
			t.Fatalf("notification exposed message text: %q", text)
		}
	}
	copyGot, copyWant := slices.Clone(got), slices.Clone(want)
	slices.Sort(copyGot)
	slices.Sort(copyWant)
	if !slices.Equal(copyGot, copyWant) {
		t.Fatalf("notifications = %q, want %q", got, want)
	}
}

func TestDesktopNotifierAcceptancePassesTextOnlyAsArgument(t *testing.T) {
	text := `1 unread from A" & do shell script "touch /tmp/unsafe"`
	mac := osascriptArgs("pairtask", text)
	wantMac := []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", "--", "pairtask", text}
	if !slices.Equal(mac, wantMac) {
		t.Fatalf("osascript arguments do not separate title and text: %q", mac)
	}
	for _, script := range mac[:len(mac)-3] {
		if strings.Contains(script, text) {
			t.Fatal("notification text was interpolated into AppleScript")
		}
	}
	linux := notifySendArgs("pairtask", text)
	if !slices.Equal(linux, []string{"--", "pairtask", text}) {
		t.Fatalf("notify-send arguments = %q", linux)
	}
}
