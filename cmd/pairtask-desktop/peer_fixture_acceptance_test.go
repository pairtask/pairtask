//go:build linux

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/pake"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/server"
	"github.com/pairtask/pairtask/internal/store"
)

// fullPeerFixture is the inviter: a real broker, pairing endpoint and TCP
// relay. Desktop acceptance tests use its operator protocol and never bypass
// pairing or message transport by writing directly to the database.
type fullPeerFixture struct {
	operatorSocket string
	destination    string
}

func startFullPeerFixture(t *testing.T) *fullPeerFixture {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "desktop-peer-b-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	last, err := st.LastSeq(context.Background())
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	h := hub.New(st, last, log)
	h.SetPairing(hub.PairingConfig{
		Exchanger: pake.New(), Name: "Full workspace", Clock: time.Now,
		Dial: func(ctx context.Context, destination string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", destination)
		},
	})
	if _, err := h.RecoverJoins(context.Background()); err != nil {
		st.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	operatorSocket := filepath.Join(dir, "operator.sock")
	pairSocket := filepath.Join(dir, "pair.sock")
	op := server.NewOperator(operatorSocket, h, os.Getuid(), log)
	pairing := server.NewPairing(pairSocket, h, log)
	go op.Listen(ctx)
	go pairing.Listen(ctx)
	awaitSocket(t, operatorSocket)
	awaitSocket(t, pairSocket)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		op.Close()
		pairing.Close()
		<-h.Done()
		st.Close()
		t.Fatal(err)
	}
	relay := peer.NewRelay(peer.RelayConfig{Dial: func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", pairSocket)
	}})
	go relay.Serve(ctx, listener)
	t.Cleanup(func() {
		cancel()
		listener.Close()
		op.Close()
		pairing.Close()
		select {
		case <-h.Done():
		case <-time.After(3 * time.Second):
			t.Error("full peer broker did not stop")
		}
		st.Close()
	})
	f := &fullPeerFixture{operatorSocket: operatorSocket, destination: listener.Addr().String()}
	f.call(t, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpStatus})
	return f
}

func awaitSocket(t *testing.T, path string) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		if time.Now().After(until) {
			t.Fatalf("socket %q did not start", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fullPeerFixture) call(t *testing.T, req protocol.Message) protocol.Message {
	t.Helper()
	if req.MessageID == "" {
		req.MessageID = uuid.NewString()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", f.operatorSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := protocol.WriteFrame(conn, req); err != nil {
		t.Fatal(err)
	}
	var resp protocol.Message
	err = protocol.ReadFrame(bufio.NewReader(conn), &resp)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type == protocol.TypeError {
		t.Fatalf("operator %s %s: %s: %s", req.Type, req.PairingOp+req.ContactOp, resp.Code, resp.Detail)
	}
	return resp
}

func (f *fullPeerFixture) openInvitation(t *testing.T) string {
	t.Helper()
	resp := f.call(t, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpOpen})
	if resp.Type != protocol.TypeOperatorPairingResult || resp.PairingResult == nil || resp.PairingResult.Invitation == nil {
		t.Fatalf("open invitation: unexpected result %#v", resp)
	}
	code := resp.PairingResult.Invitation.Code
	if code == "" {
		t.Fatal("open invitation did not return a code")
	}
	return code
}

func (f *fullPeerFixture) accept(t *testing.T, fingerprint string) {
	t.Helper()
	resp := f.call(t, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpAccept, PairingTarget: fingerprint})
	if resp.Type != protocol.TypeOperatorPairingResult {
		t.Fatal(fmt.Sprintf("accept returned %s", resp.Type))
	}
}
