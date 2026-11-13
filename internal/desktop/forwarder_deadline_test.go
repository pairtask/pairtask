package desktop

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// deadlineProbeConn cancels its call while returning a valid broker reply.
// It waits for the cancellation callback to set the deadline before Read
// returns, making the otherwise narrow successful-exchange race repeatable.
type deadlineProbeConn struct {
	cancel context.CancelFunc
	fired  chan struct{}
	once   sync.Once
	mu     sync.Mutex
	limit  time.Time
}

func (c *deadlineProbeConn) Read(p []byte) (int, error) {
	c.cancel()
	select {
	case <-c.fired:
	case <-time.After(2 * time.Second):
		return 0, errors.New("cancel callback did not set the deadline")
	}
	return copy(p, []byte(`{"type":"operator_pairing_result"}`+"\n")), nil
}

func (c *deadlineProbeConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *deadlineProbeConn) Close() error                     { return nil }
func (c *deadlineProbeConn) LocalAddr() net.Addr              { return &net.UnixAddr{Name: "test", Net: "unix"} }
func (c *deadlineProbeConn) RemoteAddr() net.Addr             { return c.LocalAddr() }
func (c *deadlineProbeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *deadlineProbeConn) SetWriteDeadline(time.Time) error { return nil }

func (c *deadlineProbeConn) SetDeadline(limit time.Time) error {
	c.mu.Lock()
	c.limit = limit
	c.mu.Unlock()
	if !limit.IsZero() {
		c.once.Do(func() { close(c.fired) })
	}
	return nil
}

func (c *deadlineProbeConn) deadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.limit
}

func TestForwarderSuccessfulCanceledCallClearsDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := &deadlineProbeConn{cancel: cancel, fired: make(chan struct{})}
	f := &forwarder{conn: conn, in: bufio.NewScanner(conn)}
	out, err := f.call(ctx, protocol.Message{Type: protocol.TypeOperatorPairing, PairingOp: protocol.PairingOpList})
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != protocol.TypeOperatorPairingResult {
		t.Fatalf("successful exchange returned %+v", out)
	}
	if limit := conn.deadline(); !limit.IsZero() {
		t.Fatalf("successful connection retained expired deadline %s", limit)
	}
}
