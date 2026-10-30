//go:build linux

package server

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// The response bytes reach the client before Write returns. This models a
// legitimate scheduler pause at the old WriteFrame/Written boundary.
type heldSessionWrite struct {
	net.Conn
	reads    atomic.Int32
	held     sync.Once
	release  chan struct{}
	nextRead chan struct{}
}

func (c *heldSessionWrite) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if err == nil && bytes.Contains(b, []byte(`"request_id":"repeatable"`)) {
		c.held.Do(func() { <-c.release })
	}
	return n, err
}
func (c *heldSessionWrite) Read(b []byte) (int, error) {
	if c.reads.Add(1) == 3 {
		close(c.nextRead)
	}
	return c.Conn.Read(b)
}

func TestOperatorSessionAcceptanceReceivedResponseAllowsIDReuseBeforeWriteReturns(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	server, client := net.Pipe()
	conn := &heldSessionWrite{Conn: server, release: make(chan struct{}), nextRead: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(conn.release) }) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { unblock(); cancel(); client.Close(); server.Close(); sessionAwaitTransport(t, done) })
	go func() {
		defer close(done)
		defer server.Close()
		f.endpoint.runSession(ctx, operatorSessionPeer{conn: conn, r: bufio.NewReader(conn), uid: os.Getuid()},
			protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "open"})
	}()
	p := &sessionProbe{operatorPeer: &operatorPeer{Conn: client, r: bufio.NewReader(client), t: t}}
	operatorEqual(t, "session opened", p.frame().text("type"), "operator_session")
	request := map[string]any{"type": "operator_snapshot", "request_id": "repeatable"}
	p.send(request)
	operatorEqual(t, "first snapshot received", p.frame().text("type"), "operator_snapshot")
	p.send(request)
	// A valid second request returns the reader to its next read. A false
	// duplicate instead closes the read loop, even though the first response
	// has already been consumed. No production sleeps or retry loops needed.
	select {
	case <-conn.nextRead:
	case <-time.After(time.Second):
		t.Fatal("received response ID was rejected while its writer returned")
	}
	unblock()
	operatorEqual(t, "reused ID succeeds", p.frame().text("type"), "operator_snapshot")
}
