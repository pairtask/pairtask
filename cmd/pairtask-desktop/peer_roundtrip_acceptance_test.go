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
	b := startFullPeerFixture(t)
	code := b.openInvitation(t)
	dir := filepath.Join(t.TempDir(), "desktop")
	a, err := desktop.Start(context.Background(), desktop.Config{Dir: dir, Name: "Desktop A"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if a != nil {
			if err := a.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	identity := desktopPairStatus(t, a).SelfIdentity
	if identity == "" {
		t.Fatal("desktop peer has no identity")
	}
	assertNoDesktopListener(t, dir)

	resp := a.Call(context.Background(), protocol.Message{Type: protocol.TypeOperatorPairing,
		PairingOp: protocol.PairingOpRedeem, PairingDestination: b.destination,
		PairingCode: code, PairingName: "Desktop A"})
	if resp.Type != protocol.TypeOperatorPairingResult || resp.PairingJoin == nil {
		t.Fatalf("redeem response: type=%s code=%s detail=%s", resp.Type, resp.Code, resp.Detail)
	}
	awaitDesktop(t, "B receives A's pairing request", func() bool {
		status := b.call(t, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpStatus})
		if status.PairingResult == nil {
			return false
		}
		for _, request := range status.PairingResult.Requests {
			if request.IdentityKey == identity {
				return true
			}
		}
		return false
	})
	b.accept(t, identity)
	var bIdentity string
	awaitDesktop(t, "A's pairing settles", func() bool {
		status := desktopPairStatus(t, a)
		for _, contact := range status.Contacts {
			if contact.Confirmed {
				bIdentity = contact.IdentityKey
				return true
			}
		}
		return false
	})
	if bIdentity == "" {
		t.Fatal("settled contact has no identity")
	}
	resp = a.Call(context.Background(), protocol.Message{Type: protocol.TypeOperatorContact,
		ContactOp: protocol.ContactOpSend, ContactKey: bIdentity, ContactText: "hello from Desktop"})
	if resp.Type != protocol.TypeOperatorContactResult || resp.ContactMessage == nil {
		t.Fatalf("Desktop send response: type=%s code=%s detail=%s", resp.Type, resp.Code, resp.Detail)
	}
	awaitDesktop(t, "B receives A's message", func() bool {
		return hasContactMessage(b.call(t, protocol.Message{Type: protocol.TypeOperatorContact,
			ContactOp: protocol.ContactOpList, ContactKey: identity}), "in", "hello from Desktop")
	})

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = nil
	b.call(t, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend,
		ContactKey: identity, ContactText: "reply while Desktop is closed"})
	a, err = desktop.Start(context.Background(), desktop.Config{Dir: dir, Name: "Desktop A"})
	if err != nil {
		t.Fatal(err)
	}
	if got := desktopPairStatus(t, a).SelfIdentity; got != identity {
		t.Fatalf("restart changed Desktop identity: got %q, want %q", got, identity)
	}
	assertNoDesktopListener(t, dir)
	awaitDesktop(t, "A receives B's queued reply after restart", func() bool {
		return hasContactMessage(a.Call(context.Background(), protocol.Message{Type: protocol.TypeOperatorContact,
			ContactOp: protocol.ContactOpList, ContactKey: bIdentity}), "in", "reply while Desktop is closed")
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
