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

// runPeerRelay forwards inbound pairing connections, sealed, to the
// broker's shared pairing socket.
func runPeerRelay(ctx context.Context, args []string, stderr io.Writer) int {
	var listen, dial string
	var bounds peerBounds
	flags := flag.NewFlagSet("peer relay", flag.ContinueOnError)
	flags.StringVar(&listen, "listen", "", "TCP address to accept pairing connections on")
	flags.StringVar(&dial, "dial-unix", "", "the broker's pairing socket")
	bounds.register(flags)
	code := parseFlags(flags, args, peerUsage, stderr, func(rest []string) error {
		if len(rest) != 0 || listen == "" || !filepath.IsAbs(dial) {
			return errors.New("peer relay needs --listen and an absolute --dial-unix")
		}
		return nil
	})
	if code != -1 {
		return code
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		fmt.Fprintln(stderr, "peer relay:", err)
		return 1
	}
	relay := peer.NewRelay(peer.RelayConfig{Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", dial)
	}, MaxConns: bounds.maxConns, MaxBytes: bounds.maxBytes, IdleTimeout: bounds.idle})
	return served(stderr, "peer relay", relay.Serve(ctx, ln))
}

// runPeerEgress serves the broker's outbound pairing connections on a
// group-shared socket this user owns.
func runPeerEgress(ctx context.Context, args []string, stderr io.Writer) int {
	var socket, group string
	var bounds peerBounds
	flags := flag.NewFlagSet("peer egress", flag.ContinueOnError)
	flags.StringVar(&socket, "socket", "", "the egress socket to create")
	flags.StringVar(&group, "group", "", "the group that may connect")
	bounds.register(flags)
	code := parseFlags(flags, args, peerUsage, stderr, func(rest []string) error {
		if len(rest) != 0 || !filepath.IsAbs(socket) || group == "" {
			return errors.New("peer egress needs an absolute --socket and --group")
		}
		return nil
	})
	if code != -1 {
		return code
	}
	ln, bound, err := listenShared(socket, group)
	if err != nil {
		fmt.Fprintln(stderr, "peer egress:", err)
		return 1
	}
	defer removeOwnSocket(socket, ln, bound)
	forwarder := egress.NewForwarder(egress.Config{MaxConns: bounds.maxConns, MaxBytes: bounds.maxBytes, IdleTimeout: bounds.idle})
	return served(stderr, "peer egress", forwarder.Serve(ctx, ln))
}

// listenShared binds a group-shared socket: its directory must be ours with
// the group and mode 0750, and an existing path is never removed.
func listenShared(path, groupName string) (*net.UnixListener, os.FileInfo, error) {
	gid, err := groupID(groupName)
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

func served(stderr io.Writer, name string, err error) int {
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	return 0
}
