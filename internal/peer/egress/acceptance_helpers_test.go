package egress_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/egress"
)

type resolveFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolveFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func routeConfig() egress.Config {
	return egress.Config{
		Resolver: resolveFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("192.168.50.7")}, nil
		}),
		LocalAddrs: func() ([]netip.Addr, error) { return nil, nil },
		Dial:       func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("fixture unreachable") },
	}
}

func routeSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "egress-test-")
	routeOK(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "egress.sock")
}

func serveRoute(t *testing.T, cfg egress.Config) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	path := routeSocket(t)
	ln, err := net.Listen("unix", path)
	routeOK(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- egress.NewForwarder(cfg).Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		ln.Close()
		select {
		case err := <-done:
			routeOK(t, err)
		case <-time.After(2 * time.Second):
			t.Error("egress workers did not stop")
		}
	})
	return path, cancel, done
}

func routeOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func rawRoute(t *testing.T, path string) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", path)
	routeOK(t, err)
	t.Cleanup(func() { c.Close() })
	routeOK(t, c.SetDeadline(time.Now().Add(time.Second)))
	return c
}

func routeHeader(destination string) []byte {
	p := make([]byte, 3+len(destination))
	p[0] = 1
	binary.BigEndian.PutUint16(p[1:3], uint16(len(destination)))
	copy(p[3:], destination)
	return p
}

func routeStatus(t *testing.T, path, destination string) byte {
	t.Helper()
	c := rawRoute(t, path)
	_, err := c.Write(routeHeader(destination))
	routeOK(t, err)
	var status [1]byte
	_, err = io.ReadFull(c, status[:])
	routeOK(t, err)
	return status[0]
}

func echoRoute(t *testing.T) net.Conn {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	go func() { defer b.Close(); io.Copy(b, b) }()
	return a
}

func assertRouteClosed(t *testing.T, c net.Conn) {
	t.Helper()
	var b [1]byte
	_, err := c.Read(b[:])
	if err == nil {
		t.Fatal("connection remained open")
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("connection was not closed: %v", err)
	}
}
