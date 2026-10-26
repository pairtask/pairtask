package peer

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// Relay is the online side: it accepts network connections and forwards
// their bytes, both ways, to a connection it dials into secured. It holds
// no certificate, no key and no parser: TLS passes through it sealed, so a
// compromised relay can drop or delay traffic but not read or forge it.
type Relay struct {
	cfg RelayConfig

	mu    sync.Mutex
	conns int
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

// Serve accepts until the context ends or the listener fails.
func (r *Relay) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !r.admit() {
			conn.Close()
			continue
		}
		go func() {
			defer r.release()
			r.forward(ctx, conn)
		}()
	}
}

func (r *Relay) admit() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conns >= r.cfg.MaxConns {
		return false
	}
	r.conns++
	return true
}

func (r *Relay) release() {
	r.mu.Lock()
	r.conns--
	r.mu.Unlock()
}

// forward copies bytes both ways until either side closes, a direction hits
// its byte bound, or the idle timeout passes.
func (r *Relay) forward(ctx context.Context, outside net.Conn) {
	defer outside.Close()
	inside, err := r.cfg.Dial(ctx)
	if err != nil {
		return
	}
	defer inside.Close()
	deadline := time.Now().Add(r.cfg.IdleTimeout)
	_ = outside.SetDeadline(deadline)
	_ = inside.SetDeadline(deadline)
	done := make(chan struct{}, 2)
	copyBounded := func(dst, src net.Conn) {
		_, _ = io.CopyN(dst, src, r.cfg.MaxBytes)
		done <- struct{}{}
	}
	go copyBounded(inside, outside)
	go copyBounded(outside, inside)
	select {
	case <-done:
	case <-ctx.Done():
	}
}
