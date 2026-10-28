//go:build linux

package server

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/pake"
	"github.com/pairtask/pairtask/internal/store"
)

type redeemLogs struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (l *redeemLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.data.Write(p)
}
func (l *redeemLogs) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.data.String() }

type redeemOperatorFixture struct {
	*operatorFixture
	st         *store.Store
	h          *hub.Hub
	pairSocket string
	logs       *redeemLogs
	stop       func()
}

func redeemConfig() hub.PairingConfig {
	return hub.PairingConfig{Exchanger: pake.New(), Name: "Bob", Clock: time.Now, Dial: func(ctx context.Context, destination string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", destination)
	}}
}
func newRedeemOperator(t *testing.T, cfg hub.PairingConfig, database string) *redeemOperatorFixture {
	t.Helper()
	if database == "" {
		database = filepath.Join(t.TempDir(), "redeem.db")
	}
	st, err := store.Open(database)
	operatorNoError(t, err)
	logs := &redeemLogs{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	seq, err := st.LastSeq(context.Background())
	operatorNoError(t, err)
	h := hub.New(st, seq, log)
	h.SetPairing(cfg)
	_, err = h.RecoverJoins(context.Background())
	operatorNoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	dir, err := os.MkdirTemp("/tmp", "pairing-server-")
	operatorNoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &redeemOperatorFixture{operatorFixture: &operatorFixture{t: t, agent: socketPath(t), operator: socketPath(t)}, st: st, h: h, pairSocket: filepath.Join(dir, "pair.sock"), logs: logs}
	f.db, err = sql.Open("sqlite", database)
	operatorNoError(t, err)
	a, o, p := New(f.agent, h, log), NewOperator(f.operator, h, os.Getuid(), log), NewPairing(f.pairSocket, h, log)
	f.endpoint = o
	done := make(chan error, 3)
	go func() { done <- a.Listen(ctx) }()
	go func() { done <- o.Listen(ctx) }()
	go func() { done <- p.Listen(ctx) }()
	var once sync.Once
	f.stop = func() {
		once.Do(func() {
			cancel()
			a.Close()
			o.Close()
			p.Close()
			waitRedeemShutdown(t, done, h.Done())
			f.db.Close()
			st.Close()
		})
	}
	t.Cleanup(f.stop)
	// Loading the identity through the public operator path is also a
	// barrier before starting a peer handshake.
	pairingSnapshot(t, pairingCommand(f.operatorFixture, "status", ""))
	waitPairingSocket(t, f.pairSocket)
	return f
}
func waitPairingSocket(t *testing.T, path string) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		if time.Now().After(until) {
			t.Fatalf("pairing socket unavailable: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func waitRedeemStarted(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("redemption worker never dialed")
	}
}
func waitRedeemShutdown(t *testing.T, done <-chan error, hubDone <-chan struct{}) {
	t.Helper()
	for range 3 {
		select {
		case err := <-done:
			operatorNoError(t, err)
		case <-time.After(3 * time.Second):
			t.Fatal("pairing listener did not stop")
		}
	}
	select {
	case <-hubDone:
	case <-time.After(3 * time.Second):
		t.Fatal("pairing worker did not settle on shutdown")
	}
}
func relayToPairing(t *testing.T, f *redeemOperatorFixture) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	operatorNoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	relay := peer.NewRelay(peer.RelayConfig{Dial: func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", f.pairSocket)
	}})
	done := make(chan error, 1)
	go func() { done <- relay.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		ln.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("relay did not stop")
		}
	})
	return ln.Addr().String()
}
func redeemCommand(f *redeemOperatorFixture, id, destination, code string) sessionWire {
	f.t.Helper()
	return roomOperator(f.operatorFixture, map[string]any{"type": "operator_pairing", "message_id": id, "pairing_op": "redeem", "pairing_destination": destination, "pairing_code": code, "pairing_name": "Bob"})
}

type redeemJoinWire struct {
	OperationID  string `json:"operation_id"`
	State        string `json:"state"`
	Destination  string `json:"destination"`
	Name         string `json:"name"`
	Peer         string `json:"peer"`
	InvitationID string `json:"invitation_id"`
}

func redeemReply(t *testing.T, r sessionWire) redeemJoinWire {
	t.Helper()
	operatorEqual(t, "redeem response: "+sessionJSON(r), r.text("type"), "operator_pairing_result")
	return sessionDecode[redeemJoinWire](t, r, "pairing_join")
}
func awaitRedeemState(t *testing.T, f *redeemOperatorFixture, id string, want peer.JoinState) peer.JoinRecord {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for {
		row, found, err := f.st.GetJoin(context.Background(), id)
		operatorNoError(t, err)
		if found && row.State == want {
			return row
		}
		if time.Now().After(until) {
			t.Fatalf("join %q state %q, want %q", id, row.State, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
