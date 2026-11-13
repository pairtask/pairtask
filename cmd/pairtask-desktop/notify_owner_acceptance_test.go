//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestDesktopNotificationAcceptanceOwnerOnlyAndNoMessageText(t *testing.T) {
	f := startDesktopNotificationFixture(t)
	f.sendInbound(t)
	requireOnlyOwnerNotification(t, f.ownerNotices, f.forwarderNotices)
}

type desktopNotificationFixture struct {
	p                *desktopPair
	forwarder        *desktop.Node
	ownerNotices     chan string
	forwarderNotices chan string
}

func startDesktopNotificationFixture(t *testing.T) *desktopNotificationFixture {
	t.Helper()
	root, err := os.MkdirTemp("", "pt-notify-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dir := filepath.Join(root, "node")
	ownerNotices, forwarderNotices := make(chan string, 4), make(chan string, 4)
	owner, err := desktop.Start(context.Background(), desktop.Config{Dir: dir, Name: "Desktop A",
		Notify: func(_ context.Context, title, text string) error {
			ownerNotices <- title + "|" + text
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	forwarder, err := desktop.Start(context.Background(), desktop.Config{Dir: dir, Name: "Desktop A",
		Notify: func(_ context.Context, title, text string) error {
			forwarderNotices <- title + "|" + text
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = forwarder.Close() })
	if !owner.Owner() || forwarder.Owner() {
		t.Fatal("Desktop owner and forwarder were not distinct")
	}
	peer := startFullPeerFixture(t)
	p := &desktopPair{a: owner, b: peer, dir: dir, identity: desktopPairStatus(t, owner).SelfIdentity}
	p.redeem(t)
	return &desktopNotificationFixture{p: p, forwarder: forwarder,
		ownerNotices: ownerNotices, forwarderNotices: forwarderNotices}
}

func (f *desktopNotificationFixture) sendInbound(t *testing.T) {
	t.Helper()
	p := f.p
	out := p.a.Call(context.Background(), protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpName,
		ContactKey: p.bIdentity, ContactText: "Colleague"})
	if out.Type == protocol.TypeError {
		t.Fatalf("name contact: %s", out.Detail)
	}
	p.b.call(t, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend,
		ContactKey: p.identity, ContactText: "PRIVATE_NOTIFICATION_BODY"})
	client := connectDesktopMCP(t, f.forwarder)
	awaitDesktop(t, "forwarder syncs inbound message", func() bool {
		var inbox desktop.InboxResult
		decodeDesktopMCP(t, desktopTool(t, client, "check_inbox", map[string]any{"from": p.bIdentity}), &inbox)
		if len(inbox.Conversations) != 1 {
			return false
		}
		for _, message := range inbox.Conversations[0].Messages {
			if message.Direction == "in" && message.Text == "PRIVATE_NOTIFICATION_BODY" {
				return true
			}
		}
		return false
	})
}

func requireOnlyOwnerNotification(t *testing.T, ownerNotices, forwarderNotices <-chan string) {
	t.Helper()
	select {
	case got := <-ownerNotices:
		assertOwnerNotification(t, got)
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not notify for the inbound message")
	}
	assertNoNotification(t, forwarderNotices, "forwarder")
	assertNoNotification(t, ownerNotices, "owner duplicate")
}

func assertOwnerNotification(t *testing.T, got string) {
	t.Helper()
	if got != "pairtask|1 unread from Colleague" {
		t.Fatalf("owner notification = %q", got)
	}
	if strings.Contains(got, "PRIVATE_NOTIFICATION_BODY") {
		t.Fatal("owner notification exposed message text")
	}
}

func assertNoNotification(t *testing.T, notices <-chan string, label string) {
	t.Helper()
	select {
	case got := <-notices:
		t.Fatalf("%s notified: %q", label, got)
	case <-time.After(200 * time.Millisecond):
	}
}
