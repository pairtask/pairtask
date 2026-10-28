//go:build linux

package server

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/firstcontact"
)

func messageAdmissionServer(t *testing.T, f *redeemOperatorFixture) *PairingServer {
	t.Helper()
	path := filepath.Join(filepath.Dir(f.pairSocket), "admission.sock")
	p := NewPairing(path, f.h, testLog())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Listen(ctx) }()
	t.Cleanup(func() {
		cancel()
		p.Close()
		select {
		case err := <-done:
			operatorNoError(t, err)
		case <-time.After(3 * time.Second):
			t.Error("admission server did not stop")
		}
	})
	waitPairingSocket(t, path)
	return p
}
func awaitAdmissionSlots(t *testing.T, slots chan struct{}, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(slots) != want {
		if time.Now().After(deadline) {
			t.Fatalf("slots=%d want=%d", len(slots), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestMessageAdmissionAcceptanceHandshakeCapAndRecovery(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	p := messageAdmissionServer(t, f)
	var raws []net.Conn
	for i := 0; i < 8; i++ {
		raw, err := net.Dial("unix", p.socketPath)
		operatorNoError(t, err)
		raws = append(raws, raw)
		t.Cleanup(func() { raw.Close() })
	}
	awaitAdmissionSlots(t, p.handshakes, 8)
	extra, err := net.Dial("unix", p.socketPath)
	operatorNoError(t, err)
	defer extra.Close()
	operatorNoError(t, extra.SetReadDeadline(time.Now().Add(time.Second)))
	var b [1]byte
	_, err = extra.Read(b[:])
	if err == nil {
		t.Fatal("ninth unauthenticated connection remained open")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("over-limit handshake waited instead of closing")
	}
	for _, raw := range raws {
		raw.Close()
	}
	awaitAdmissionSlots(t, p.handshakes, 0)
	raw, err := net.Dial("unix", p.socketPath)
	operatorNoError(t, err)
	defer raw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := firstcontact.Client(ctx, raw, messageIdentity(t), time.Now)
	operatorNoError(t, err)
	defer conn.Close()
}
