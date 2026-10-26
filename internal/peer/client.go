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
	Cert      tls.Certificate // this workspace's identity (fixture-only until #115)
	ServerPin string          // the gate's expected fingerprint
	// AllowUnpinned lets the client connect with an empty ServerPin and
	// record what it saw. It is a probe switch, not a first-contact policy;
	// that policy is open in #178.
	AllowUnpinned bool
	Timeout       time.Duration
}

// Dial connects to addr and completes the handshake.
func Dial(ctx context.Context, addr string, cfg ClientConfig) (*Client, error) {
	if cfg.ServerPin == "" && !cfg.AllowUnpinned {
		return nil, errors.New("a server pin is required unless AllowUnpinned is set")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	raw, err := (&net.Dialer{Timeout: cfg.Timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return handshake(ctx, raw, cfg)
}

// handshake wraps an open connection; tests use it with any net.Conn.
func handshake(ctx context.Context, raw net.Conn, cfg ClientConfig) (*Client, error) {
	c := &Client{timeout: cfg.Timeout}
	tlsCfg := &tls.Config{
		Certificates:       []tls.Certificate{cfg.Cert},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // the pin below replaces chain verification: these are self-signed fixtures
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("the gate presented no certificate")
			}
			cert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			c.fingerprint = Fingerprint(cert)
			if cfg.ServerPin != "" && c.fingerprint != cfg.ServerPin {
				return fmt.Errorf("the gate's fingerprint %s doesn't match the pin", c.fingerprint[:16])
			}
			return nil
		},
	}
	conn := tls.Client(raw, tlsCfg)
	_ = conn.SetDeadline(time.Now().Add(cfg.Timeout))
	if err := conn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	c.conn, c.r = conn, bufio.NewReader(conn)
	return c, nil
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
