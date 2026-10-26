package peer

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// Client is the asking side: it connects through a relay, verifies the gate
// by its pinned fingerprint, and sends records.
type Client struct {
	conn        *tls.Conn
	r           *bufio.Reader
	fingerprint string
	timeout     time.Duration
}

// ClientConfig is what a client needs.
type ClientConfig struct {
	Cert tls.Certificate // this workspace's identity (fixture-only until #115)
	// Server is the gate's exact certificate. It is the only trusted root,
	// its first DNS name is the expected server name, and the presented leaf
	// must be byte-for-byte this certificate. How a client first obtains and
	// trusts it is open in #178; the probe pre-trusts a fixture.
	Server  *x509.Certificate
	Timeout time.Duration
}

// Dial connects to addr and completes the handshake.
func Dial(ctx context.Context, addr string, cfg ClientConfig) (*Client, error) {
	tlsCfg, err := clientTLS(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	raw, err := (&net.Dialer{Timeout: cfg.Timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, tlsCfg)
	_ = conn.SetDeadline(time.Now().Add(cfg.Timeout))
	if err := conn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return &Client{conn: conn, r: bufio.NewReader(conn), fingerprint: Fingerprint(cfg.Server), timeout: cfg.Timeout}, nil
}

// clientTLS trusts exactly the configured server certificate: standard
// chain and hostname verification against it as the sole root, then an
// exact match of the presented leaf, so no other certificate, renewed or
// issued from it, inherits that trust.
func clientTLS(cfg ClientConfig) (*tls.Config, error) {
	if cfg.Server == nil {
		return nil, errors.New("the gate's certificate is required")
	}
	if len(cfg.Server.DNSNames) == 0 {
		return nil, errors.New("the gate's certificate has no DNS name to verify")
	}
	roots := x509.NewCertPool()
	roots.AddCert(cfg.Server)
	want := Fingerprint(cfg.Server)
	return &tls.Config{
		Certificates: []tls.Certificate{cfg.Cert},
		MinVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		ServerName:   cfg.Server.DNSNames[0],
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || Fingerprint(state.PeerCertificates[0]) != want {
				return errors.New("the gate presented a different certificate")
			}
			return nil
		},
	}, nil
}

// Send writes one record and reads its reply.
func (c *Client) Send(ctx context.Context, rec Record) (Reply, error) {
	deadline := time.Now().Add(c.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.conn.SetDeadline(deadline)
	if err := WriteJSON(c.conn, rec); err != nil {
		return Reply{}, err
	}
	line, err := readBoundedLine(c.r, MaxRecordBytes)
	if err != nil {
		return Reply{}, err
	}
	var reply Reply
	if err := json.Unmarshal(line, &reply); err != nil {
		return Reply{}, fmt.Errorf("malformed reply: %w", err)
	}
	return reply, reply.matches(rec)
}

// matches checks that a reply answers the record that was sent.
func (r Reply) matches(rec Record) error {
	switch {
	case r.Version != SchemaVersion:
		return fmt.Errorf("reply version %d is not %d", r.Version, SchemaVersion)
	case r.RequestID != rec.RequestID:
		return errors.New("the reply answers a different request")
	case !knownStatus[r.Status]:
		return fmt.Errorf("unknown reply status %q", r.Status)
	}
	return nil
}

var knownStatus = map[string]bool{StatusPending: true, StatusAccepted: true, StatusIgnored: true, StatusDuplicate: true, StatusRefused: true}

// ServerFingerprint is the gate's certificate fingerprint as seen at the
// handshake.
func (c *Client) ServerFingerprint() string { return c.fingerprint }

// Close ends the connection.
func (c *Client) Close() error { return c.conn.Close() }
