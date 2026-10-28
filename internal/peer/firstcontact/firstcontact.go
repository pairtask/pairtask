// Package firstcontact is the TLS 1.3 connection two workspaces open to pair
// (#178). Neither end trusts the other yet: each presents a fresh
// self-signed certificate for its workspace key, and the pairing exchange
// run over the connection, bound to both presented keys and to the
// connection's exporter, is what authenticates it.
//
// A Conn is UNAUTHENTICATED until a pairing exchange over it succeeds. TLS
// alone enrolls nothing: it only fixes which keys and which connection the
// exchange must bind.
package firstcontact

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

const (
	// ALPN names the pairing exchange; both ends must negotiate it.
	ALPN = "pairtask-pairing/2"
	// ContactALPN names a connection between paired workspaces, each pinning
	// the other's identity; no exchange runs on it.
	ContactALPN = "pairtask-contact/1"
	// MessageALPN names a message session between confirmed contacts (#210),
	// each pinning the other's identity; a peer that doesn't speak it
	// refuses the handshake, so nothing reaches its settlement.
	MessageALPN = "pairtask-message/1"
	// MaxMessageFrameBytes bounds a message session's frames.
	MaxMessageFrameBytes = 32 << 10
	// ExporterLabel is RFC 9266's tls-exporter channel binding.
	ExporterLabel = "EXPORTER-Channel-Binding"
	// ChannelBindingBytes is the exporter's length.
	ChannelBindingBytes = 32
	// CertificateValidity is how long each presented certificate is valid.
	CertificateValidity = time.Hour
	// HandshakeTimeout bounds a handshake whose context has no sooner deadline.
	HandshakeTimeout = 10 * time.Second

	certificateName = "pairtask-workspace"
)

// ErrFirstContact is a refused endpoint, handshake or peer certificate.
var ErrFirstContact = errors.New("firstcontact: refused")

// Endpoint is where to listen or dial: a TCP network and address.
type Endpoint struct {
	Network, Address string
}

func (e Endpoint) check() error {
	switch e.Network {
	case "tcp", "tcp4", "tcp6":
		return nil
	}
	return fmt.Errorf("%w: unsupported network %q", ErrFirstContact, e.Network)
}

func checkDeps(id *peer.WorkspaceIdentity, clock func() time.Time) error {
	if id == nil || clock == nil {
		return fmt.Errorf("%w: needs a workspace identity and a clock", ErrFirstContact)
	}
	return nil
}

// Conn is one first-contact connection.
type Conn struct {
	tls      *tls.Conn
	conn     peer.Connection
	protocol string
}

// ServerOptions widen what a server accepts. ContactPins, when set, also
// offers contact connections, accepting one only from an identity it
// reports true for.
type ServerOptions struct {
	ContactPins func(fingerprint string) bool
	// MessagePins, when set, also offers message sessions, accepting one
	// only from an identity it reports true for: a confirmed contact.
	MessagePins func(fingerprint string) bool
}

// Protocol is the negotiated ALPN: ALPN, ContactALPN or MessageALPN.
func (c *Conn) Protocol() string { return c.protocol }

// Server runs the inviter's end over raw, which it owns: on any error raw is
// closed.
func Server(ctx context.Context, raw net.Conn, id *peer.WorkspaceIdentity, clock func() time.Time) (*Conn, error) {
	return handshake(ctx, raw, id, clock, pairingRole(true))
}

// ServerWith is Server with options, such as accepting contact connections.
func ServerWith(ctx context.Context, raw net.Conn, id *peer.WorkspaceIdentity, clock func() time.Time, opts ServerOptions) (*Conn, error) {
	return handshake(ctx, raw, id, clock, serverRole(opts))
}

// Client runs the joiner's end over raw, which it owns: on any error raw is
// closed.
func Client(ctx context.Context, raw net.Conn, id *peer.WorkspaceIdentity, clock func() time.Time) (*Conn, error) {
	return handshake(ctx, raw, id, clock, pairingRole(false))
}

// ContactClient opens a contact connection to the workspace whose identity
// is pin, refusing any other; raw is owned as by Client.
func ContactClient(ctx context.Context, raw net.Conn, id *peer.WorkspaceIdentity, clock func() time.Time, pin string) (*Conn, error) {
	return handshake(ctx, raw, id, clock, contactRole(pin))
}

// MessageClient opens a message session's connection to the contact whose
// identity is pin, refusing any other; raw is owned as by Client.
func MessageClient(ctx context.Context, raw net.Conn, id *peer.WorkspaceIdentity, clock func() time.Time, pin string) (*Conn, error) {
	return handshake(ctx, raw, id, clock, pinnedRole(MessageALPN, pin))
}

// NoCommonProtocol reports a handshake the peer refused for want of a
// protocol in common, by its TLS alert: an older workspace without message
// sessions, as opposed to one that couldn't be reached or presented another
// identity. crypto/tls reports a peer's alert as a "remote error" OpError
// whose error is the alert's text.
func NoCommonProtocol(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "remote error" && op.Err != nil && op.Err.Error() == "tls: no application protocol"
}

func handshake(ctx context.Context, raw net.Conn, id *peer.WorkspaceIdentity, clock func() time.Time, role tlsRole) (*Conn, error) {
	if err := checkDeps(id, clock); err != nil {
		raw.Close()
		return nil, err
	}
	own, err := id.Certificate(certificateName, clock(), CertificateValidity)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("%w: certificate: %w", ErrFirstContact, err)
	}
	cfg := selfSignedTLSConfig(own, id.Fingerprint(), clock, role)
	t := tls.Client(raw, cfg)
	if role.server {
		t = tls.Server(raw, cfg)
	}
	ctx, cancel := context.WithTimeout(ctx, HandshakeTimeout)
	defer cancel()
	// On failure raw is closed directly: a TLS close would wait to send
	// close_notify to a peer that may not be reading.
	if err := t.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("%w: handshake: %w", ErrFirstContact, err)
	}
	c, err := established(t)
	if err != nil {
		raw.Close()
		return nil, err
	}
	return c, nil
}

// established reads the finished connection's peer key and exporter.
func established(t *tls.Conn) (*Conn, error) {
	cs := t.ConnectionState()
	ekm, err := cs.ExportKeyingMaterial(ExporterLabel, nil, ChannelBindingBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: channel binding: %w", ErrFirstContact, err)
	}
	return &Conn{tls: t, conn: peer.Connection{PeerLeaf: peer.SPKIFingerprint(cs.PeerCertificates[0]), ChannelBinding: ekm},
		protocol: cs.NegotiatedProtocol}, nil
}

// Connection is what this connection fixes for the pairing exchange: the
// peer's presented key and the exporter, as a copy.
func (c *Conn) Connection() peer.Connection {
	return peer.Connection{PeerLeaf: c.conn.PeerLeaf, ChannelBinding: append([]byte(nil), c.conn.ChannelBinding...)}
}

// MessageFrames carries a message session's frames, as Frames does but up
// to MaxMessageFrameBytes each.
func (c *Conn) MessageFrames(ctx context.Context) peer.Frames {
	return streamFrames{ctx: ctx, conn: c.tls, limit: MaxMessageFrameBytes}
}

// Frames carries pairing frames, each a 4-byte big-endian length and its
// bytes. ctx bounds every send and receive: its deadline applies, and
// cancelling it interrupts a blocked one. Use one sender and one receiver at
// a time.
func (c *Conn) Frames(ctx context.Context) peer.Frames {
	return peer.BoundFrames(streamFrames{ctx: ctx, conn: c.tls})
}

// Close closes the connection.
func (c *Conn) Close() error { return c.tls.Close() }

// Dial connects to an inviter and completes the handshake.
func Dial(ctx context.Context, ep Endpoint, id *peer.WorkspaceIdentity, clock func() time.Time) (*Conn, error) {
	if err := ep.check(); err != nil {
		return nil, err
	}
	if err := checkDeps(id, clock); err != nil {
		return nil, err
	}
	var d net.Dialer
	raw, err := d.DialContext(ctx, ep.Network, ep.Address)
	if err != nil {
		return nil, fmt.Errorf("%w: dial: %w", ErrFirstContact, err)
	}
	return Client(ctx, raw, id, clock)
}
