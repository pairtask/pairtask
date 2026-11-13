package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// forwarder is a copy that doesn't own the data directory: it hands each
// call to the owner over the socket, one at a time.
type forwarder struct {
	mu   sync.Mutex
	conn net.Conn
	in   *bufio.Scanner
}

// errBroken: an earlier call on this connection failed, so what it would
// read next may be another call's answer.
var errBroken = errors.New("the connection to the shared node failed")

// dialOwner connects to the owner's socket in dir, if it is ours.
func dialOwner(ctx context.Context, dir string) (*forwarder, error) {
	sock, err := socketPath(dir)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return nil, err
	}
	if uid, err := peerUID(c); err != nil || uid != os.Geteuid() {
		c.Close()
		return nil, errors.Join(errors.New("the shared node isn't ours"), err)
	}
	in := bufio.NewScanner(c)
	in.Buffer(make([]byte, 0, 64<<10), maxLine)
	return &forwarder{conn: c, in: in}, nil
}

// call sends one request and reads its answer, within ctx. Any failure
// closes the connection, so a late answer is never read as another's.
func (f *forwarder) call(ctx context.Context, msg protocol.Message) (protocol.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	conn := f.conn
	if conn == nil {
		return protocol.Message{}, errBroken
	}
	cut := cancelOn(ctx, conn)
	out, err := f.exchange(msg)
	if cut() && err == nil {
		conn.SetDeadline(time.Time{}) // the answer came in time; the connection stays usable
	}
	if err != nil {
		conn.Close()
		f.conn = nil
	}
	return out, err
}

// cancelOn ends conn's pending I/O when ctx ends. The function it returns
// stops that, and reports whether ctx's end had already reached conn, once
// that has finished: a deadline set then must be cleared before reuse.
func cancelOn(ctx context.Context, conn net.Conn) func() bool {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		conn.SetDeadline(time.Now())
	})
	return func() bool {
		if stop() {
			return false
		}
		<-done
		return true
	}
}

// exchange writes the request and reads one answer.
func (f *forwarder) exchange(msg protocol.Message) (protocol.Message, error) {
	var out protocol.Message
	if err := writeLine(f.conn, msg); err != nil {
		return out, err
	}
	if !f.in.Scan() {
		return out, errors.Join(io.ErrUnexpectedEOF, f.in.Err())
	}
	return out, json.Unmarshal(f.in.Bytes(), &out)
}

// close drops the connection.
func (f *forwarder) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn != nil {
		f.conn.Close()
		f.conn = nil
	}
}
