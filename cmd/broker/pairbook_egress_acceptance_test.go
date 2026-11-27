package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/egress"
	"github.com/pairtask/pairtask/internal/space"
)

type directoryResolver struct{}

func (directoryResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if host != "example.com" {
		return nil, fmt.Errorf("unexpected host %s", host)
	}
	return []netip.Addr{netip.MustParseAddr("100.95.1.2")}, nil
}

func TestPairbookEnrollmentThroughConnector(t *testing.T) {
	id := strings.Repeat("a", 32)
	directory := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/spaces/"+id+"/enrollments" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprintf(w, `{"enrollment":%q}`, id)
	}))
	defer directory.Close()
	path := startDirectoryConnector(t, directory.Listener.Addr().String())
	if pairbookHTTP(config{}) != nil {
		t.Fatal("direct mode should use the directory client defaults")
	}
	httpClient := pairbookHTTP(config{PairingEgressSocket: path})
	roots := x509.NewCertPool()
	roots.AddCert(directory.Certificate())
	transport := httpClient.Transport.(*http.Transport)
	transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	defer transport.CloseIdleConnections()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const base = "https://example.com"
	client, err := space.NewClient(base, key, space.Options{HTTP: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := client.StartEnroll(context.Background(), space.Pin{Space: id, Base: base}, "Box")
	if err != nil || enrollment.ID != id {
		t.Fatalf("enrollment %q: %v", enrollment.ID, err)
	}
}

func startDirectoryConnector(t *testing.T, target string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "egress.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	forwarder := egress.NewForwarder(egress.Config{Resolver: directoryResolver{}, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "100.95.1.2:443" {
			return nil, fmt.Errorf("unexpected destination %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}})
	done := make(chan error, 1)
	go func() { done <- forwarder.Serve(ctx, listener) }()
	t.Cleanup(func() { cancel(); listener.Close(); <-done })
	return path
}
