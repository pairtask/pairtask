package peer

import (
	"context"
	"errors"
	"io"
	"net"
	"time"
)

// Relay is the online side: it accepts network connections and forwards
// their bytes, both ways, to a connection it dials into secured. It holds
// no certificate, no key and no parser: TLS passes through it sealed, so a
// compromised relay can drop or delay traffic but not read or forge it.
type Relay struct {
	cfg RelayConfig

	live liveConns
}

// RelayConfig is what a relay needs: how to reach the gate, and bounds.
type RelayConfig struct {
	Dial        func(ctx context.Context) (net.Conn, error) // into secured, typically a Unix socket
	MaxConns    int
	MaxBytes    int64 // per direction per connection; 0 for the default
	IdleTimeout time.Duration
}

const (
	defaultRelayConns = 16
	defaultRelayBytes = 1 << 20
	defaultRelayIdle  = 30 * time.Second
)

// NewRelay applies defaults. A relay without a Dial refuses every
// connection, since it has nowhere to forward to.
func NewRelay(cfg RelayConfig) *Relay {
	if cfg.Dial == nil {
		cfg.Dial = func(context.Context) (net.Conn, error) { return nil, errors.New("the relay has no dial target") }
	}
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = defaultRelayConns
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = defaultRelayBytes
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultRelayIdle
	}
	return &Relay{cfg: cfg}
}

// Serve accepts until the context ends or the listener fails. Ending the
// context closes the listener and every live connection, and Serve returns
// once their workers have finished.
func (r *Relay) Serve(ctx context.Context, ln net.Listener) error {
	return r.live.serve(ctx, ln, r.cfg.MaxConns, r.forward)
}

// forward copies bytes both ways until either side closes, a direction hits
// its byte bound, or no bytes move for the idle timeout.
func (r *Relay) forward(ctx context.Context, outside net.Conn) {
	defer outside.Close()
	inside, err := r.cfg.Dial(ctx)
	if err != nil {
		return
	}
	defer inside.Close()
	r.live.add(inside) // so cancellation closes the inner side too
	defer r.live.remove(inside)
	idle := &idlePair{a: outside, b: inside, timeout: r.cfg.IdleTimeout}
	idle.touch()
	done := make(chan struct{}, 2)
	go func() { copyBounded(inside, outside, r.cfg.MaxBytes, idle); done <- struct{}{} }()
	go func() { copyBounded(outside, inside, r.cfg.MaxBytes, idle); done <- struct{}{} }()
	<-done
}

// copyBounded copies up to limit bytes, extending the pair's idle deadline
// after every read, so the timeout measures inactivity.
func copyBounded(dst io.Writer, src io.Reader, limit int64, idle *idlePair) {
	buf := make([]byte, 32<<10)
	var total int64
	for total < limit {
		n, err := src.Read(buf[:min(int64(len(buf)), limit-total)])
		if n > 0 {
			idle.touch()
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
			total += int64(n)
		}
		if err != nil {
			return
		}
	}
}

// idlePair keeps two connections on one inactivity deadline.
type idlePair struct {
	a, b    net.Conn
	timeout time.Duration
}

func (p *idlePair) touch() {
	deadline := time.Now().Add(p.timeout)
	_ = p.a.SetDeadline(deadline)
	_ = p.b.SetDeadline(deadline)
}
