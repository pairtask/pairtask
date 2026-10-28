package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/egress"
	"github.com/pairtask/pairtask/internal/peer/shared"
)

// The pairing route's connector side (#178, experimental). Neither command
// holds a key or reads what it carries: TLS and the pairing exchange run
// end to end between the brokers. Both run until SIGINT or SIGTERM and are
// started by nothing by default.

const peerUsage = "usage: pairtask peer relay --listen ADDR --dial-unix PATH [--max-conns N] [--max-bytes N] [--idle 30s]\n" +
	"       pairtask peer egress --socket PATH --group NAME [--max-conns N] [--max-bytes N] [--idle 30s]"

// peerBounds are a forwarder's limits; zero takes peer.Relay's defaults.
type peerBounds struct {
	maxConns int
	maxBytes int64
	idle     time.Duration
}

func (b *peerBounds) register(flags *flag.FlagSet) {
	flags.IntVar(&b.maxConns, "max-conns", 0, "concurrent connections (default 16)")
	flags.Int64Var(&b.maxBytes, "max-bytes", 0, "bytes per direction per connection (default 1 MiB)")
	flags.DurationVar(&b.idle, "idle", 0, "idle timeout (default 30s)")
}

func runPeer(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	commands := map[string]func(context.Context, []string, io.Writer) int{"relay": runPeerRelay, "egress": runPeerEgress}
	if len(args) == 0 || commands[args[0]] == nil {
		fmt.Fprintln(stderr, peerUsage)
		return 2
	}
	return commands[args[0]](ctx, args[1:], stderr)
}

// relayArgs are peer relay's settings.
type relayArgs struct {
	listen, dial string
	bounds       peerBounds
}

func (a relayArgs) check(rest []string) error {
	if len(rest) != 0 {
		return errors.New("peer relay takes no arguments")
	}
	if a.listen == "" {
		return errors.New("peer relay needs --listen")
	}
	if !filepath.IsAbs(a.dial) {
		return errors.New("peer relay needs an absolute --dial-unix")
	}
	return nil
}

// runPeerRelay forwards inbound pairing connections, sealed, to the
// broker's shared pairing socket.
func runPeerRelay(ctx context.Context, args []string, stderr io.Writer) int {
	var a relayArgs
	flags := flag.NewFlagSet("peer relay", flag.ContinueOnError)
	flags.StringVar(&a.listen, "listen", "", "TCP address to accept pairing connections on")
	flags.StringVar(&a.dial, "dial-unix", "", "the broker's pairing socket")
	a.bounds.register(flags)
	// A closure, not a.check: a method value would copy a before parsing.
	if code := parseFlags(flags, args, peerUsage, stderr, func(rest []string) error { return a.check(rest) }); code != -1 {
		return code
	}
	ln, err := net.Listen("tcp", a.listen)
	if err != nil {
		return failed(stderr, daemonRelay, err)
	}
	dial := a.dial
	relay := peer.NewRelay(peer.RelayConfig{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", dial)
	}, MaxConns: a.bounds.maxConns, MaxBytes: a.bounds.maxBytes, IdleTimeout: a.bounds.idle})
	return served(stderr, daemonRelay, relay.Serve(ctx, ln))
}

// egressArgs are peer egress's settings.
type egressArgs struct {
	socket, group string
	bounds        peerBounds
}

func (a egressArgs) check(rest []string) error {
	if len(rest) != 0 {
		return errors.New("peer egress takes no arguments")
	}
	if !filepath.IsAbs(a.socket) {
		return errors.New("peer egress needs an absolute --socket")
	}
	if a.group == "" {
		return errors.New("peer egress needs --group")
	}
	return nil
}

// runPeerEgress serves the broker's outbound pairing connections on a
// group-shared socket this user owns.
func runPeerEgress(ctx context.Context, args []string, stderr io.Writer) int {
	var a egressArgs
	flags := flag.NewFlagSet("peer egress", flag.ContinueOnError)
	flags.StringVar(&a.socket, "socket", "", "the egress socket to create")
	flags.StringVar(&a.group, "group", "", "the group that may connect")
	a.bounds.register(flags)
	// A closure, not a.check: a method value would copy a before parsing.
	if code := parseFlags(flags, args, peerUsage, stderr, func(rest []string) error { return a.check(rest) }); code != -1 {
		return code
	}
	ln, bound, err := listenShared(a)
	if err != nil {
		return failed(stderr, daemonEgress, err)
	}
	defer removeOwnSocket(a.socket, ln, bound)
	forwarder := egress.NewForwarder(egress.Config{MaxConns: a.bounds.maxConns, MaxBytes: a.bounds.maxBytes, IdleTimeout: a.bounds.idle})
	return served(stderr, daemonEgress, forwarder.Serve(ctx, ln))
}

// listenShared binds a group-shared socket: its directory must be ours with
// the group and mode 0750, and an existing path is never removed.
func listenShared(a egressArgs) (*net.UnixListener, os.FileInfo, error) {
	path := a.socket
	gid, err := groupID(a.group)
	if err != nil {
		return nil, nil, err
	}
	if err := shared.CheckSharedDir(filepath.Dir(path), os.Geteuid(), gid); err != nil {
		return nil, nil, err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("socket path already exists: %s", path)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, nil, err
	}
	ln.SetUnlinkOnClose(false)
	bound, err := os.Lstat(path)
	if err == nil {
		err = shared.SecureSharedSocket(path, gid)
	}
	if err != nil {
		removeOwnSocket(path, ln, bound)
		return nil, nil, err
	}
	return ln, bound, nil
}

func groupID(name string) (int, error) {
	group, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(group.Gid)
}

// removeOwnSocket removes the socket this process bound, and nothing that
// replaced it.
func removeOwnSocket(path string, ln *net.UnixListener, bound os.FileInfo) {
	ln.Close()
	if info, err := os.Lstat(path); err == nil && bound != nil && os.SameFile(bound, info) {
		os.Remove(path)
	}
}

// daemon names a peer command in its messages.
type daemon string

const (
	daemonRelay  daemon = "peer relay"
	daemonEgress daemon = "peer egress"
)

func failed(stderr io.Writer, d daemon, err error) int {
	fmt.Fprintf(stderr, "%s: %v\n", d, err)
	return 1
}

func served(stderr io.Writer, d daemon, err error) int {
	if err != nil {
		return failed(stderr, d, err)
	}
	return 0
}
