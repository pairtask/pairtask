package desktop_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/desktop"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type sharedNode struct {
	n        *desktop.Node
	once     sync.Once
	closeErr error
}

func startSharedNode(t *testing.T, dir string) *sharedNode {
	t.Helper()
	n, err := desktop.Start(context.Background(), desktop.Config{Dir: dir, Name: "Claude Mac"})
	if err != nil {
		t.Fatal(err)
	}
	s := &sharedNode{n: n}
	t.Cleanup(func() { _ = s.close() })
	return s
}

func (s *sharedNode) close() error {
	s.once.Do(func() { s.closeErr = s.n.Close() })
	return s.closeErr
}

func sharedIdentity(t *testing.T, n *desktop.Node) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := n.Call(ctx, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpList})
	if out.Type != protocol.TypeOperatorPairingResult {
		t.Fatalf("pairing request through node returned %+v", out)
	}
	if out.PairingResult == nil {
		t.Fatal("pairing reply has no snapshot")
	}
	if out.PairingResult.SelfIdentity == "" {
		t.Fatal("pairing reply has no workspace identity")
	}
	return out.PairingResult.SelfIdentity
}

func TestSharedDesktopNodeAcceptanceForwarderAndTakeover(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "node")
	first := startSharedNode(t, dir)
	second := startSharedNode(t, dir)
	if !first.n.Owner() {
		t.Fatal("first node did not own the database")
	}
	if second.n.Owner() {
		t.Fatal("second node tried to own the same database")
	}
	identity := sharedIdentity(t, first.n)
	if got := sharedIdentity(t, second.n); got != identity {
		t.Fatalf("forwarder saw identity %q, want %q", got, identity)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	if got := sharedIdentity(t, second.n); got != identity {
		t.Fatalf("takeover changed identity to %q, want %q", got, identity)
	}
	if !second.n.Owner() {
		t.Fatal("forwarder did not take over after owner exit")
	}
}

func TestSharedDesktopNodeAcceptanceOneForwarderWinsTakeover(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "node")
	owner := startSharedNode(t, dir)
	a := startSharedNode(t, dir)
	b := startSharedNode(t, dir)
	identity := sharedIdentity(t, owner.n)
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	for _, got := range concurrentSharedIdentities(a.n, b.n) {
		if got != identity {
			t.Fatalf("forwarder did not answer after concurrent takeover: %q", got)
		}
	}
	assertOneSharedOwner(t, a.n, b.n)
}

func concurrentSharedIdentities(a, b *desktop.Node) [2]string {
	answers := make(chan string, 2)
	go func() { answers <- sharedIdentityAnswer(a) }()
	go func() { answers <- sharedIdentityAnswer(b) }()
	return [2]string{<-answers, <-answers}
}

func sharedIdentityAnswer(n *desktop.Node) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := n.Call(ctx, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpList})
	if out.PairingResult == nil {
		return ""
	}
	return out.PairingResult.SelfIdentity
}

func assertOneSharedOwner(t *testing.T, a, b *desktop.Node) {
	t.Helper()
	owners := 0
	for _, n := range []*desktop.Node{a, b} {
		if n.Owner() {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("%d nodes claimed the database after takeover", owners)
	}
}

func TestSharedDesktopNodeAcceptanceStaleSocketAndPrivateMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "node")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, desktop.SocketFile)
	if err := os.WriteFile(socket, []byte("stale socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := startSharedNode(t, dir)
	if !n.n.Owner() {
		t.Fatal("stale socket prevented the new owner")
	}
	info, err := os.Lstat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("node path is not a socket: %v", info.Mode())
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("node socket mode is %o, want 0600", info.Mode().Perm())
	}
}

func TestSharedDesktopNodeAcceptanceLockWithoutOwnerExplainsRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "node")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := store.LockDatabase(filepath.Join(dir, "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	start := time.Now()
	_, err = desktop.Start(ctx, desktop.Config{Dir: dir, Name: "Claude Mac"})
	if !errors.Is(err, desktop.ErrNoOwner) {
		t.Fatalf("missing owner returned %v, want ErrNoOwner", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("missing owner took too long to explain recovery")
	}
}

func TestSharedDesktopNodeAcceptanceOversizeCallDropsOnlyThatConnection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "node")
	n := startSharedNode(t, dir)
	conn, err := net.Dial("unix", filepath.Join(dir, desktop.SocketFile))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// This is valid JSON if read without a limit. A 1 MiB line must instead
	// close just this connection before it can reach the broker.
	payload := fmt.Sprintf(`{"type":"operator_pairing","pairing_op":"list","message_id":"oversize","padding":"%s"}`+"\n",
		strings.Repeat("x", 1<<20))
	_, _ = conn.Write([]byte(payload))
	var one [1]byte
	count, err := conn.Read(one[:])
	if count != 0 {
		t.Fatalf("oversize call received a broker answer: n=%d err=%v", count, err)
	}
	if err == nil {
		t.Fatal("oversize call remained readable")
	}
	if os.IsTimeout(err) {
		t.Fatal("oversize call left its connection open")
	}
	sharedIdentity(t, n.n)
}
