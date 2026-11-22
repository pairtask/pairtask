//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/pake"
	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/server"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/store"
)

type flowNode struct {
	h                       *hub.Hub
	st                      *store.Store
	keys                    *personkey.Keys
	keyDir, socket, address string
	id                      *peer.WorkspaceIdentity
}

func startFlowNode(t *testing.T, d *flowDirectory) *flowNode {
	t.Helper()
	dir, err := os.MkdirTemp("", "space-cli-")
	offlineOK(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	n := &flowNode{keyDir: filepath.Join(dir, "identity"), socket: filepath.Join(dir, "op")}
	n.keys, err = personkey.Create(n.keyDir)
	offlineOK(t, err)
	_, _, err = personkey.CreateAnchor(n.keyDir, false, time.Now().Add(-time.Hour))
	offlineOK(t, err)
	n.st, err = store.Open(filepath.Join(dir, "broker.db"))
	offlineOK(t, err)
	log := slog.New(slog.DiscardHandler)
	n.h = hub.New(n.st, 0, log)
	n.h.SetPairing(hub.PairingConfig{Exchanger: pake.New(), Name: "test workspace", Dial: func(ctx context.Context, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}})
	n.h.SetSpaces(hub.SpaceConfig{Maps: func(_ string, s crypto.Signer) (space.MapDirectory, error) {
		return flowClient{d: d, key: s.Public().(ed25519.PublicKey)}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	go n.h.Run(ctx)
	op := server.NewOperator(n.socket, n.h, os.Getuid(), log)
	pairPath := filepath.Join(dir, "pair")
	ps := server.NewPairing(pairPath, n.h, log)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	offlineOK(t, err)
	n.address = ln.Addr().String()
	relay := peer.NewRelay(peer.RelayConfig{Dial: func(ctx context.Context) (net.Conn, error) { return (&net.Dialer{}).DialContext(ctx, "unix", pairPath) }})
	done := make(chan error, 3)
	go func() { done <- relay.Serve(ctx, ln) }()
	go func() { done <- op.Listen(ctx) }()
	go func() { done <- ps.Listen(ctx) }()
	t.Cleanup(func() {
		cancel()
		op.Close()
		ps.Close()
		ln.Close()
		for range 3 {
			<-done
		}
		<-n.h.Done()
		n.st.Close()
	})
	for _, path := range []string{n.socket, pairPath} {
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(path); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("socket never appeared: %s", path)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	n.command(t, "status")
	ep, err := n.h.PairingEndpoint()
	offlineOK(t, err)
	n.id = ep.Identity
	return n
}
func (n *flowNode) command(t *testing.T, args ...string) string {
	t.Helper()
	t.Setenv(personkey.DirEnv, n.keyDir)
	argv := append([]string{"operator", "space"}, args...)
	argv = append(argv, "--socket", n.socket, "--json")
	var out, stderr bytes.Buffer
	code := run(context.Background(), argv, &out, &stderr)
	if code != 0 {
		t.Fatalf("space %s: exit %d: %s", args[0], code, stderr.String())
	}
	return out.String()
}
