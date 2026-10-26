package peer

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests use real TCP and Unix sockets on one host. They do not establish
// an Incus UID boundary or prove that the gate's process has no network access.
type acceptanceWire struct {
	mu         sync.Mutex
	data       bytes.Buffer
	tamperNext atomic.Bool
}

func (w *acceptanceWire) add(p []byte) { w.mu.Lock(); defer w.mu.Unlock(); w.data.Write(p) }
func (w *acceptanceWire) snapshot() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Clone(w.data.Bytes())
}

type acceptanceTap struct {
	net.Conn
	wire *acceptanceWire
}

func (c *acceptanceTap) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.wire.add(p[:n])
	return n, err
}
func (c *acceptanceTap) Write(p []byte) (int, error) {
	if len(p) > 0 && c.wire.tamperNext.Swap(false) {
		p = bytes.Clone(p)
		p[len(p)-1] ^= 1
	}
	n, err := c.Conn.Write(p)
	c.wire.add(p[:n])
	return n, err
}

type acceptanceProbe struct {
	addr       string
	serverCert tls.Certificate
	wire       *acceptanceWire
	store      Store
	stop       func()
}

func acceptanceCertificate(t *testing.T, name string) tls.Certificate {
	t.Helper()
	c, err := SelfSigned(name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func acceptanceFingerprint(t *testing.T, cert tls.Certificate) string {
	return Fingerprint(acceptanceLeaf(t, cert))
}

func acceptanceLeaf(t *testing.T, cert tls.Certificate) *x509.Certificate {
	t.Helper()
	c, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func acceptanceDefaults(t *testing.T, config GateConfig) GateConfig {
	t.Helper()
	if len(config.Cert.Certificate) == 0 {
		config.Cert = acceptanceCertificate(t, "gate")
	}
	if config.Store == nil {
		config.Store = NewMemStore()
	}
	if config.Discoverable == nil {
		config.Discoverable = func() bool { return true }
	}
	return config
}

func acceptanceNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func acceptanceEqual[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func acceptanceListen(t *testing.T, network, address string) net.Listener {
	t.Helper()
	ln, err := net.Listen(network, address)
	acceptanceNoError(t, err)
	t.Cleanup(func() { ln.Close() })
	return ln
}

func acceptanceStart(t *testing.T, config GateConfig) *acceptanceProbe {
	t.Helper()
	config = acceptanceDefaults(t, config)
	dir, err := os.MkdirTemp("", "ptls-")
	acceptanceNoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	unix := acceptanceListen(t, "unix", filepath.Join(dir, "peer.sock"))
	gate, err := NewGate(config)
	acceptanceNoError(t, err)
	tcp := acceptanceListen(t, "tcp", "127.0.0.1:0")
	wire := &acceptanceWire{}
	relay := NewRelay(RelayConfig{Dial: func(ctx context.Context) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, "unix", unix.Addr().String())
		if err != nil {
			return nil, err
		}
		return &acceptanceTap{Conn: c, wire: wire}, nil
	}, IdleTimeout: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	gateDone, relayDone := make(chan error, 1), make(chan error, 1)
	go func() { gateDone <- gate.Serve(ctx, unix) }()
	go func() { relayDone <- relay.Serve(ctx, tcp) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			unix.Close()
			tcp.Close()
			for name, done := range map[string]<-chan error{"gate": gateDone, "relay": relayDone} {
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Errorf("%s failed to stop", name)
				}
			}
		})
	}
	t.Cleanup(stop)
	return &acceptanceProbe{addr: tcp.Addr().String(), serverCert: config.Cert, wire: wire, store: config.Store, stop: stop}
}

func (p *acceptanceProbe) connect(t *testing.T, cert tls.Certificate) *tls.Conn {
	t.Helper()
	raw, err := net.DialTimeout("tcp", p.addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	pin := acceptanceFingerprint(t, p.serverCert)
	server := acceptanceLeaf(t, p.serverCert)
	roots := x509.NewCertPool()
	roots.AddCert(server)
	c := tls.Client(raw, &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		// This fixture pretrusts the gate certificate; real enrollment is open.
		RootCAs:    roots,
		ServerName: server.DNSNames[0],
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 || Fingerprint(state.PeerCertificates[0]) != pin {
				return fmt.Errorf("server pin mismatch")
			}
			return nil
		},
	})
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if err := c.Handshake(); err != nil {
		t.Fatal(err)
	}
	return c
}

func acceptanceRecord(id string) Record {
	return Record{Version: SchemaVersion, Kind: Kind("contact_request"), RequestID: id, From: Identity{Name: "Alice", Workspace: "work"}, Note: "private note: 71fdaa18-8f42-4233-b471-6c7ede4fb720"}
}

func acceptanceSend(t *testing.T, c net.Conn, record Record) Reply {
	t.Helper()
	if err := json.NewEncoder(c).Encode(record); err != nil {
		t.Fatal(err)
	}
	var reply Reply
	if err := json.NewDecoder(c).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

func acceptanceStatus(t *testing.T, reply Reply, want string) {
	t.Helper()
	if reply.Status != want {
		t.Fatalf("status = %q, want %q; reply=%+v", reply.Status, want, reply)
	}
}
