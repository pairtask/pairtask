// Package egress lets a broker with no network of its own reach another
// workspace for pairing (#178). connector runs a Forwarder on a Unix socket
// the broker can connect to; per connection the broker names one
// destination, the forwarder checks it, dials it and then copies opaque
// bytes both ways. TLS and the pairing exchange run end to end between the
// brokers, so the forwarder sees the destination but never the contents,
// and it holds no key.
//
// Request: a version byte (1), a big-endian uint16 length (1 to 255) and the
// destination, host:port. Reply: one status byte, then the tunnel when it is
// StatusConnected.
//
// The forwarder refuses destinations that resolve to forbidden address
// classes: loopback, link-local, interface-local or other multicast,
// unspecified, and this host's own interface addresses. It is a bound on
// where a pairing redemption may go, not a general SSRF defence: private
// LAN and tailnet addresses are allowed, since those are where other
// workspaces live.
package egress

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

const (
	// Version is the request format.
	Version byte = 1

	StatusConnected  byte = 0
	StatusRefused    byte = 1
	StatusDialFailed byte = 2

	// HeaderTimeout bounds reading a request and writing its status.
	HeaderTimeout = 5 * time.Second
	// DialTimeout bounds resolving and dialing a destination.
	DialTimeout = 10 * time.Second
)

var (
	// ErrRefused: the destination is malformed or in a forbidden class.
	ErrRefused = errors.New("egress: destination refused")
	// ErrDialFailed: the forwarder could not connect to the destination.
	ErrDialFailed = errors.New("egress: dial failed")
	// ErrProtocol: a malformed request or reply.
	ErrProtocol = errors.New("egress: protocol error")
)

// Resolver looks up a host's addresses; *net.Resolver is one.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Config wires a forwarder. Nil functions take the system's; zero bounds
// take peer.Relay's defaults.
type Config struct {
	Resolver    Resolver
	Dial        func(ctx context.Context, network, address string) (net.Conn, error)
	LocalAddrs  func() ([]netip.Addr, error)
	MaxConns    int
	MaxBytes    int64
	IdleTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.Resolver == nil {
		c.Resolver = net.DefaultResolver
	}
	if c.Dial == nil {
		var d net.Dialer
		c.Dial = d.DialContext
	}
	if c.LocalAddrs == nil {
		c.LocalAddrs = interfaceAddrs
	}
	return c
}

// interfaceAddrs are this host's own addresses.
func interfaceAddrs() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range addrs {
		if prefix, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, prefix.Addr().Unmap())
		}
	}
	return out, nil
}

// Forwarder serves egress requests.
type Forwarder struct {
	cfg   Config
	relay *peer.Relay
}

// NewForwarder applies defaults.
func NewForwarder(cfg Config) *Forwarder {
	f := &Forwarder{cfg: cfg.withDefaults()}
	f.relay = peer.NewRelay(peer.RelayConfig{Connect: f.connect, MaxConns: cfg.MaxConns, MaxBytes: cfg.MaxBytes, IdleTimeout: cfg.IdleTimeout})
	return f
}

// Serve accepts until ctx ends or the listener fails. Connections beyond
// the bound are closed before anything is read; ending ctx closes every
// connection, half-open ones included, and Serve returns once they have
// ended.
func (f *Forwarder) Serve(ctx context.Context, ln net.Listener) error {
	return f.relay.Serve(ctx, ln)
}

// connect reads the request, checks and dials its destination, and answers
// with the status before any byte is copied.
func (f *Forwarder) connect(ctx context.Context, client net.Conn) (net.Conn, error) {
	destination, err := readRequest(client)
	if errors.Is(err, ErrRefused) {
		return nil, errors.Join(err, reply(client, StatusRefused))
	}
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, DialTimeout)
	defer cancel()
	target, err := f.resolve(ctx, destination)
	if err != nil {
		return nil, errors.Join(err, reply(client, StatusRefused))
	}
	conn, err := f.cfg.Dial(ctx, "tcp", target)
	if err != nil {
		return nil, errors.Join(ErrDialFailed, err, reply(client, StatusDialFailed))
	}
	if err := reply(client, StatusConnected); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// readRequest reads one request within HeaderTimeout. A bad version is a
// protocol error, closed without a reply; an empty or oversized length is
// refused from the header alone, before any destination byte is read.
func readRequest(client net.Conn) (string, error) {
	if err := client.SetReadDeadline(time.Now().Add(HeaderTimeout)); err != nil {
		return "", err
	}
	var head [3]byte
	if _, err := io.ReadFull(client, head[:]); err != nil {
		return "", fmt.Errorf("%w: %w", ErrProtocol, err)
	}
	if head[0] != Version {
		return "", fmt.Errorf("%w: version %d", ErrProtocol, head[0])
	}
	size := int(binary.BigEndian.Uint16(head[1:]))
	if size == 0 || size > peer.MaxDestinationBytes {
		return "", fmt.Errorf("%w: destination of %d bytes", ErrRefused, size)
	}
	destination := make([]byte, size)
	if _, err := io.ReadFull(client, destination); err != nil {
		return "", fmt.Errorf("%w: %w", ErrProtocol, err)
	}
	return string(destination), client.SetReadDeadline(time.Time{})
}

// reply writes a status within HeaderTimeout and clears the deadline, so it
// never cuts short the tunnel that follows.
func reply(client net.Conn, status byte) error {
	if err := client.SetWriteDeadline(time.Now().Add(HeaderTimeout)); err != nil {
		return err
	}
	if _, err := client.Write([]byte{status}); err != nil {
		return err
	}
	return client.SetWriteDeadline(time.Time{})
}

// resolve resolves the destination once and returns the first address as a
// literal, so the dial does no lookup of its own. Any forbidden address
// refuses the whole destination; so does a failed or empty lookup.
func (f *Forwarder) resolve(ctx context.Context, destination string) (string, error) {
	host, port, err := peer.SplitDestination(destination)
	if err != nil {
		return "", ErrRefused
	}
	addrs, err := f.lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		return "", ErrRefused
	}
	local, err := f.cfg.LocalAddrs()
	if err != nil {
		return "", ErrRefused
	}
	if slices.ContainsFunc(addrs, func(a netip.Addr) bool { return forbidden(a, local) }) {
		return "", ErrRefused
	}
	return netip.AddrPortFrom(addrs[0].Unmap(), port).String(), nil
}

func (f *Forwarder) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	return f.cfg.Resolver.LookupNetIP(ctx, "ip", host)
}

// forbiddenClasses are the address classes no redemption may reach.
var forbiddenClasses = []func(netip.Addr) bool{
	netip.Addr.IsLoopback,
	netip.Addr.IsLinkLocalUnicast,
	netip.Addr.IsLinkLocalMulticast,
	netip.Addr.IsInterfaceLocalMulticast,
	netip.Addr.IsMulticast,
	netip.Addr.IsUnspecified,
	func(a netip.Addr) bool { return a.Zone() != "" },
}

func forbidden(a netip.Addr, local []netip.Addr) bool {
	a = a.Unmap()
	if slices.Contains(local, a.WithZone("")) {
		return true
	}
	return slices.ContainsFunc(forbiddenClasses, func(class func(netip.Addr) bool) bool { return class(a) })
}

// Dial is the broker's side: it asks the forwarder at socketPath for a
// connection to destination and returns it once the forwarder connected.
// Any other or missing reply is an error, and no TLS is attempted on it.
func Dial(ctx context.Context, socketPath, destination string) (net.Conn, error) {
	if err := peer.CheckDestination(destination); err != nil {
		return nil, ErrRefused
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, err
	}
	// Cancelling ctx closes a connection still waiting for its status.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	status, err := request(conn, destination)
	if !stop() {
		conn.Close()
		return nil, errors.Join(ctx.Err(), err)
	}
	if err == nil {
		err = statusError(status)
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func request(conn net.Conn, destination string) (byte, error) {
	frame := binary.BigEndian.AppendUint16([]byte{Version}, uint16(len(destination)))
	if _, err := conn.Write(append(frame, destination...)); err != nil {
		return 0, err
	}
	var status [1]byte
	if _, err := io.ReadFull(conn, status[:]); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrProtocol, err)
	}
	return status[0], nil
}

var statusErrors = map[byte]error{StatusConnected: nil, StatusRefused: ErrRefused, StatusDialFailed: ErrDialFailed}

func statusError(status byte) error {
	err, known := statusErrors[status]
	if !known {
		return fmt.Errorf("%w: unknown status %d", ErrProtocol, status)
	}
	return err
}
