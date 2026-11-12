//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestDesktopPeerAcceptanceRedeemExchangeAndRestart(t *testing.T) {
	p := newDesktopPair(t)
	p.redeem(t)
	p.exchange(t)
	p.restartAndReceive(t)
}

type desktopPair struct {
	a         *desktop.Node
	b         *fullPeerFixture
	dir       string
	identity  string
	bIdentity string
}

func newDesktopPair(t *testing.T) *desktopPair {
	t.Helper()
	b := startFullPeerFixture(t)
	dir := filepath.Join(t.TempDir(), "desktop")
	a, err := desktop.Start(context.Background(), desktop.Config{Dir: dir, Name: "Desktop A"})
	if err != nil {
		t.Fatal(err)
	}
	p := &desktopPair{a: a, b: b, dir: dir}
	t.Cleanup(func() {
		if p.a != nil {
			if err := p.a.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	p.identity = desktopPairStatus(t, a).SelfIdentity
	if p.identity == "" {
		t.Fatal("desktop peer has no identity")
	}
	assertNoDesktopListener(t, dir)
	return p
}

func (p *desktopPair) redeem(t *testing.T) {
	t.Helper()
	resp := p.a.Call(context.Background(), protocol.Message{Type: protocol.TypeOperatorPairing,
		PairingOp: protocol.PairingOpRedeem, PairingDestination: p.b.destination,
		PairingCode: p.b.openInvitation(t), PairingName: "Desktop A"})
	if resp.Type != protocol.TypeOperatorPairingResult || resp.PairingJoin == nil {
		t.Fatalf("redeem response: type=%s code=%s detail=%s", resp.Type, resp.Code, resp.Detail)
	}
	awaitDesktop(t, "B receives A's pairing request", func() bool {
		status := p.b.call(t, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpStatus})
		return hasPairingRequest(status, p.identity)
	})
	p.b.accept(t, p.identity)
	awaitDesktop(t, "A's pairing settles", func() bool {
		p.bIdentity = confirmedContact(desktopPairStatus(t, p.a))
		return p.bIdentity != ""
	})
	assertNoDesktopListener(t, p.dir)
}

func hasPairingRequest(status protocol.Message, identity string) bool {
	if status.PairingResult == nil {
		return false
	}
	for _, request := range status.PairingResult.Requests {
		if request.IdentityKey == identity {
			return true
		}
	}
	return false
}

func confirmedContact(status protocol.PairingSnapshot) string {
	for _, contact := range status.Contacts {
		if contact.Confirmed {
			return contact.IdentityKey
		}
	}
	return ""
}

func (p *desktopPair) exchange(t *testing.T) {
	t.Helper()
	resp := p.a.Call(context.Background(), protocol.Message{Type: protocol.TypeOperatorContact,
		ContactOp: protocol.ContactOpSend, ContactKey: p.bIdentity, ContactText: "hello from Desktop"})
	if resp.Type != protocol.TypeOperatorContactResult || resp.ContactMessage == nil {
		t.Fatalf("Desktop send response: type=%s code=%s detail=%s", resp.Type, resp.Code, resp.Detail)
	}
	awaitDesktop(t, "B receives A's message", func() bool {
		return hasContactMessage(p.b.call(t, protocol.Message{Type: protocol.TypeOperatorContact,
			ContactOp: protocol.ContactOpList, ContactKey: p.identity}), "in", "hello from Desktop")
	})
}

func (p *desktopPair) restartAndReceive(t *testing.T) {
	t.Helper()
	if err := p.a.Close(); err != nil {
		t.Fatal(err)
	}
	p.a = nil
	p.b.call(t, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend,
		ContactKey: p.identity, ContactText: "reply while Desktop is closed"})
	a, err := desktop.Start(context.Background(), desktop.Config{Dir: p.dir, Name: "Desktop A"})
	if err != nil {
		t.Fatal(err)
	}
	p.a = a
	if got := desktopPairStatus(t, a).SelfIdentity; got != p.identity {
		t.Fatalf("restart changed Desktop identity: got %q, want %q", got, p.identity)
	}
	assertNoDesktopListener(t, p.dir)
	awaitDesktop(t, "A receives B's queued reply after restart", func() bool {
		return hasContactMessage(a.Call(context.Background(), protocol.Message{Type: protocol.TypeOperatorContact,
			ContactOp: protocol.ContactOpList, ContactKey: p.bIdentity}), "in", "reply while Desktop is closed")
	})
}

func desktopPairStatus(t *testing.T, a *desktop.Node) protocol.PairingSnapshot {
	t.Helper()
	resp := a.Call(context.Background(), protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpStatus})
	if resp.Type != protocol.TypeOperatorPairingResult || resp.PairingResult == nil {
		t.Fatalf("Desktop pairing status: type=%s code=%s detail=%s", resp.Type, resp.Code, resp.Detail)
	}
	return *resp.PairingResult
}

func hasContactMessage(resp protocol.Message, direction, text string) bool {
	if resp.Type != protocol.TypeOperatorContactResult || resp.ContactPage == nil {
		return false
	}
	for _, message := range resp.ContactPage.Messages {
		if message.Direction == direction && message.Text == text {
			return true
		}
	}
	return false
}

func awaitDesktop(t *testing.T, what string, check func() bool) {
	t.Helper()
	until := time.Now().Add(15 * time.Second)
	for !check() {
		if time.Now().After(until) {
			t.Fatal("timed out: " + what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func assertNoDesktopListener(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSocket != 0 {
			t.Fatalf("Desktop opened a listener: %s", entry.Name())
		}
	}
}

func TestDesktopPeerAcceptanceRefusesUnsafeDataDirectory(t *testing.T) {
	for _, kind := range []string{"world-readable", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir, actual := unsafeDesktopDataDir(t, kind)
			node, err := desktop.Start(context.Background(), desktop.Config{Dir: dir, Name: "Desktop A"})
			if err == nil {
				node.Close()
				t.Fatal("Desktop accepted an unsafe data directory")
			}
			if _, err := os.Lstat(filepath.Join(actual, "broker.db")); !os.IsNotExist(err) {
				t.Fatalf("Desktop wrote a database before refusing the data directory: %v", err)
			}
		})
	}
}

func unsafeDesktopDataDir(t *testing.T, kind string) (string, string) {
	t.Helper()
	base := t.TempDir()
	actual := filepath.Join(base, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	if kind == "world-readable" {
		if err := os.Chmod(actual, 0o755); err != nil {
			t.Fatal(err)
		}
		return actual, actual
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	return link, actual
}
