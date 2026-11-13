package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer/pake"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// SocketFile is where the owning copy shares its broker, in the data
// directory: one JSON operator request per line, then its answer.
const SocketFile = "node.sock"

// maxLine bounds one request or answer on the socket.
const maxLine = 1 << 20

// ErrSocketPathTooLong: the data directory's path leaves no room for the
// socket's, so its owner can't share the node.
var ErrSocketPathTooLong = errors.New("the data folder's path is too long to share the node between Claude surfaces: pass a shorter --dir")

// maxSocketPath is the longest socket path the system binds: sun_path
// less its terminating NUL.
func maxSocketPath() int {
	if runtime.GOOS == "darwin" {
		return 103
	}
	return 107
}

// socketPath is where dir's owner shares its node, or ErrSocketPathTooLong.
func socketPath(dir string) (string, error) {
	sock := filepath.Join(dir, SocketFile)
	if len(sock) > maxSocketPath() {
		return sock, ErrSocketPathTooLong
	}
	return sock, nil
}

// owner is the copy that holds the database's lock: it runs the broker and
// answers the other copies' calls on the socket.
type owner struct {
	hub    *hub.Hub
	st     *store.Store
	lock   *store.DatabaseLock
	ctx    context.Context // the broker's; canceled when it stops
	cancel context.CancelFunc
	ln     net.Listener
	sock   string
	log    *slog.Logger
	mu     sync.Mutex
	conns  map[net.Conn]struct{} // nil once closing
	wg     sync.WaitGroup
}

// startOwner takes the lock, opens the database, runs the broker under
// base and shares it; store.ErrDatabaseInUse means another copy owns it.
func startOwner(ctx, base context.Context, cfg Config) (*owner, error) {
	db := filepath.Join(cfg.Dir, databaseFile)
	lock, err := store.LockDatabase(db)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(db)
	if err != nil {
		lock.Close()
		return nil, err
	}
	o := &owner{st: st, lock: lock, log: cfg.Log, conns: map[net.Conn]struct{}{}}
	if err := o.run(ctx, base, cfg); err != nil {
		return nil, errors.Join(err, st.Close(), lock.Close())
	}
	if err := o.share(cfg.Dir); err != nil {
		return nil, errors.Join(err, o.stop())
	}
	if cfg.Notify != nil {
		o.wg.Add(1)
		go o.watch(cfg.Notify)
	}
	return o, nil
}

// run builds the hub over the open store and starts it.
func (o *owner) run(ctx, base context.Context, cfg Config) error {
	lastSeq, err := o.st.LastSeq(ctx)
	if err != nil {
		return err
	}
	h := hub.New(o.st, lastSeq, cfg.Log)
	h.SetPairing(hub.PairingConfig{Dial: dialer(cfg.Dial), Exchanger: pake.New(), Name: cfg.Name})
	if _, err := h.RecoverJoins(ctx); err != nil {
		return fmt.Errorf("recover pairing redemptions: %w", err)
	}
	o.ctx, o.cancel = context.WithCancel(base)
	o.hub = h
	go h.Run(o.ctx)
	return nil
}

// dialer is dial, or a direct TCP dial without one.
func dialer(dial func(ctx context.Context, destination string) (net.Conn, error)) func(ctx context.Context, destination string) (net.Conn, error) {
	if dial != nil {
		return dial
	}
	return func(ctx context.Context, destination string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", destination)
	}
}

// share listens on the data directory's socket, replacing one a former
// owner left: the lock says none is running. A path too long to bind
// leaves the node unshared, as it was before sharing existed.
func (o *owner) share(dir string) error {
	sock, err := socketPath(dir)
	if err != nil {
		o.log.Warn("this node can't be shared", "error", err, "socket", sock)
		return nil
	}
	o.sock = sock
	if err := os.Remove(o.sock); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("share the node: %w", err)
	}
	ln, err := net.Listen("unix", o.sock)
	if err != nil {
		return fmt.Errorf("share the node: %w", err)
	}
	if err := os.Chmod(o.sock, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("share the node: %w", err)
	}
	o.ln = ln
	go o.serve()
	return nil
}

// serve answers each connection of ours until the listener closes.
func (o *owner) serve() {
	for {
		c, err := o.ln.Accept()
		if err != nil {
			return
		}
		if !o.track(c) {
			c.Close()
			return
		}
		go o.handle(c)
	}
}

// track keeps a connection to close with the owner; false once closing.
func (o *owner) track(c net.Conn) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conns == nil {
		return false
	}
	o.conns[c] = struct{}{}
	o.wg.Add(1)
	return true
}

func (o *owner) untrack(c net.Conn) {
	o.mu.Lock()
	delete(o.conns, c)
	o.mu.Unlock()
	c.Close()
	o.wg.Done()
}

// handle answers one connection's requests in order, from our own user
// only: anything else, or a line too long or unreadable, ends it.
func (o *owner) handle(c net.Conn) {
	defer o.untrack(c)
	if uid, err := peerUID(c); err != nil || uid != os.Geteuid() {
		o.log.Warn("refused a connection to the shared node", "error", err)
		return
	}
	in := bufio.NewScanner(c)
	in.Buffer(make([]byte, 0, 64<<10), maxLine)
	for in.Scan() {
		var msg protocol.Message
		if err := json.Unmarshal(in.Bytes(), &msg); err != nil {
			return
		}
		if err := writeLine(c, o.call(o.ctx, msg)); err != nil {
			return
		}
	}
}

// writeLine sends one message and its newline.
func writeLine(c net.Conn, msg protocol.Message) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.Write(append(b, '\n'))
	return err
}

// call hands a request to the broker as this user.
func (o *owner) call(ctx context.Context, msg protocol.Message) protocol.Message {
	return o.hub.SubmitOperator(ctx, os.Getuid(), msg)
}

// close stops sharing (the socket goes while the lock is still held, so
// no copy reaches a stopping broker), then stops the broker.
func (o *owner) close() error {
	if o.ln != nil {
		o.ln.Close()
		os.Remove(o.sock)
	}
	o.mu.Lock()
	for c := range o.conns {
		c.Close()
	}
	o.conns = nil
	o.mu.Unlock()
	return o.stop()
}

// stop ends the broker and releases the database.
func (o *owner) stop() error {
	o.cancel()
	o.wg.Wait()
	<-o.hub.Done()
	return errors.Join(o.st.Close(), o.lock.Close())
}
