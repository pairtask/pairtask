package firstcontact

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// Listener accepts first-contact connections.
type Listener struct {
	tcp   *net.TCPListener
	id    *peer.WorkspaceIdentity
	clock func() time.Time
}

// Listen listens on a TCP endpoint.
func Listen(ep Endpoint, id *peer.WorkspaceIdentity, clock func() time.Time) (*Listener, error) {
	if err := ep.check(); err != nil {
		return nil, err
	}
	if err := checkDeps(id, clock); err != nil {
		return nil, err
	}
	ln, err := net.Listen(ep.Network, ep.Address)
	if err != nil {
		return nil, fmt.Errorf("%w: listen: %w", ErrFirstContact, err)
	}
	return &Listener{tcp: ln.(*net.TCPListener), id: id, clock: clock}, nil
}

// Accept waits for one connection and completes its handshake. Cancelling
// ctx interrupts the wait without closing the listener. Call it from one
// goroutine at a time.
func (l *Listener) Accept(ctx context.Context) (*Conn, error) {
	var raw net.Conn
	err := guarded(ctx, l.tcp.SetDeadline, func() error {
		var err error
		raw, err = l.tcp.Accept()
		return err
	})
	if err != nil {
		return nil, err
	}
	return Server(ctx, raw, l.id, l.clock)
}

// Addr is the listening address.
func (l *Listener) Addr() net.Addr { return l.tcp.Addr() }

// Close stops listening.
func (l *Listener) Close() error { return l.tcp.Close() }
