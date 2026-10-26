package peer

import (
	"context"
	"net"
	"sync"
)

// liveConns is an accept loop's bookkeeping: a bound on concurrent
// connections, the set of live ones so cancellation can close them, and a
// wait for their workers.
type liveConns struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{} // accepted connections, counted against the bound
	inner map[net.Conn]struct{} // connections workers opened themselves; closed on cancel, not counted
	wg    sync.WaitGroup
}

// serve accepts from ln and runs handle for each admitted connection until
// ctx ends or the listener fails; it returns after every worker has ended.
func (l *liveConns) serve(ctx context.Context, ln net.Listener, maxConns int, handle func(context.Context, net.Conn)) error {
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			ln.Close()
			l.closeAll()
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		l.wg.Wait()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !l.admit(conn, maxConns) {
			conn.Close() // over the bound: refused before anything is read
			continue
		}
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			defer l.remove(conn)
			handle(ctx, conn)
		}()
	}
}

func (l *liveConns) admit(conn net.Conn, maxConns int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.conns) >= maxConns {
		return false
	}
	if l.conns == nil {
		l.conns = map[net.Conn]struct{}{}
	}
	l.conns[conn] = struct{}{}
	return true
}

// add tracks a connection a worker opened itself, so cancellation closes it
// too; it doesn't count against the bound.
func (l *liveConns) add(conn net.Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inner == nil {
		l.inner = map[net.Conn]struct{}{}
	}
	l.inner[conn] = struct{}{}
}

func (l *liveConns) remove(conn net.Conn) {
	l.mu.Lock()
	delete(l.conns, conn)
	delete(l.inner, conn)
	l.mu.Unlock()
}

func (l *liveConns) closeAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for conn := range l.conns {
		conn.Close()
	}
	for conn := range l.inner {
		conn.Close()
	}
}
