//go:build linux

package main

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/server"
)

type offlineWire struct {
	agent, operator string
	hub             *hub.Hub
	server          *server.Server
	owner           *server.OperatorServer
	cancel          context.CancelFunc
	done            chan error
}

func (f *offlineInboxFixture) wire(t *testing.T) *offlineWire {
	t.Helper()
	dir, err := os.MkdirTemp("", "inbox-offline-wire-")
	offlineOK(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	db := f.open(t)
	seq, err := db.LastSeq(context.Background())
	offlineOK(t, err)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	w := &offlineWire{agent: filepath.Join(dir, "agent.sock"), operator: filepath.Join(dir, "operator.sock"), hub: hub.New(db, seq, log), cancel: cancel, done: make(chan error, 2)}
	w.server, w.owner = server.New(w.agent, w.hub, log), server.NewOperator(w.operator, w.hub, os.Getuid(), log)
	go w.hub.Run(ctx)
	go func() { w.done <- w.server.Listen(ctx) }()
	go func() { w.done <- w.owner.Listen(ctx) }()
	t.Cleanup(func() { w.stop(t) })
	return w
}

func (w *offlineWire) stop(t *testing.T) {
	t.Helper()
	w.cancel()
	w.server.Close()
	w.owner.Close()
	select {
	case <-w.hub.Done():
	case <-time.After(5 * time.Second):
		t.Error("offline fixture hub did not stop")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-w.done:
		case <-time.After(5 * time.Second):
			t.Error("offline fixture endpoint did not stop")
		}
	}
}

type offlineWirePeer struct {
	net.Conn
	reader *bufio.Reader
}

func offlineDial(t *testing.T, path string) *offlineWirePeer {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			t.Cleanup(func() { conn.Close() })
			return &offlineWirePeer{Conn: conn, reader: bufio.NewReader(conn)}
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (p *offlineWirePeer) exchange(t *testing.T, msg protocol.Message) protocol.Message {
	t.Helper()
	offlineOK(t, p.SetDeadline(time.Now().Add(5*time.Second)))
	offlineOK(t, protocol.WriteFrame(p.Conn, msg))
	var response protocol.Message
	offlineOK(t, protocol.ReadFrame(p.reader, &response))
	return response
}
