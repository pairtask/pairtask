package egress_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/egress"
)

func TestEgressAcceptanceForbiddenAddressesNeverDial(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "127.2.3.4", "::1", "::ffff:127.0.0.1", "0.0.0.0", "::", "169.254.169.254", "fe80::1", "224.0.0.1", "ff02::1", "ff01::1", "192.168.50.9"} {
		t.Run(address, func(t *testing.T) {
			cfg := routeConfig()
			cfg.Resolver = resolveFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr(address)}, nil
			})
			cfg.LocalAddrs = func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("192.168.50.9")}, nil }
			var calls atomic.Int32
			cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
				calls.Add(1)
				return nil, errors.New("must not dial")
			}
			path, _, _ := serveRoute(t, cfg)
			if status := routeStatus(t, path, "peer.example:443"); status != egress.StatusRefused {
				t.Fatalf("status=%d, want refused", status)
			}
			if calls.Load() != 0 {
				t.Fatal("forbidden destination reached TCP dial")
			}
		})
	}
}

func TestEgressAcceptanceMixedDNSAnswerRefusedAsAWhole(t *testing.T) {
	cfg := routeConfig()
	cfg.Resolver = resolveFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.168.50.7"), netip.MustParseAddr("127.0.0.1")}, nil
	})
	var calls atomic.Int32
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("must not dial")
	}
	path, _, _ := serveRoute(t, cfg)
	if routeStatus(t, path, "mixed.example:443") != egress.StatusRefused || calls.Load() != 0 {
		t.Fatal("mixed DNS answer was allowed")
	}
}

func TestEgressAcceptanceResolvesOnceAndDialsValidatedLiteral(t *testing.T) {
	for _, address := range []string{"192.168.50.7", "100.100.20.30", "fd00::123", "::ffff:192.168.50.7"} {
		t.Run(address, func(t *testing.T) { assertValidatedDial(t, address) })
	}
}

func assertValidatedDial(t *testing.T, address string) {
	t.Helper()
	cfg := routeConfig()
	var lookups atomic.Int32
	cfg.Resolver = resolveFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr(address)}, nil
	})
	dialed := make(chan string, 1)
	cfg.Dial = func(_ context.Context, _, destination string) (net.Conn, error) {
		dialed <- destination
		return echoRoute(t), nil
	}
	path, _, _ := serveRoute(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := egress.Dial(ctx, path, "peer.example:8443")
	routeOK(t, err)
	defer c.Close()
	routeOK(t, c.SetDeadline(time.Now().Add(time.Second)))
	want := net.JoinHostPort(netip.MustParseAddr(address).Unmap().String(), "8443")
	if got := <-dialed; got != want {
		t.Fatalf("dialed %q, want validated IP %q", got, want)
	}
	if lookups.Load() != 1 {
		t.Fatalf("DNS lookups=%d", lookups.Load())
	}
	_, err = c.Write([]byte("opaque"))
	routeOK(t, err)
	p := make([]byte, 6)
	_, err = io.ReadFull(c, p)
	routeOK(t, err)
	if string(p) != "opaque" {
		t.Fatal("tunnel changed bytes")
	}
}

func TestEgressAcceptanceLookupFailuresNeverDial(t *testing.T) {
	for _, failure := range []string{"DNS error", "empty DNS", "interface error"} {
		t.Run(failure, func(t *testing.T) {
			cfg := failedLookupConfig(failure)
			var calls atomic.Int32
			cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
				calls.Add(1)
				return nil, errors.New("must not dial")
			}
			path, _, _ := serveRoute(t, cfg)
			if routeStatus(t, path, "peer.example:443") == egress.StatusConnected {
				t.Fatal("lookup failure was allowed")
			}
			if calls.Load() != 0 {
				t.Fatal("lookup failure still dialed")
			}
		})
	}
}

func TestEgressAcceptancePortParsingNeverDialsZero(t *testing.T) {
	cfg := routeConfig()
	dialed := make(chan string, 1)
	cfg.Dial = func(_ context.Context, _, address string) (net.Conn, error) {
		dialed <- address
		return nil, errors.New("fixture unreachable")
	}
	path, _, _ := serveRoute(t, cfg)
	status := routeStatus(t, path, "peer.example:+443")
	select {
	case address := <-dialed:
		if address != "192.168.50.7:443" {
			t.Fatalf("validated port changed during dial: %s", address)
		}
	default:
		if status != egress.StatusRefused {
			t.Fatalf("signed port neither refused nor dialed correctly: %d", status)
		}
	}
}

func failedLookupConfig(failure string) egress.Config {
	cfg := routeConfig()
	switch failure {
	case "DNS error":
		cfg.Resolver = resolveFunc(func(context.Context, string, string) ([]netip.Addr, error) { return nil, errors.New("lookup failed") })
	case "empty DNS":
		cfg.Resolver = resolveFunc(func(context.Context, string, string) ([]netip.Addr, error) { return nil, nil })
	case "interface error":
		cfg.LocalAddrs = func() ([]netip.Addr, error) { return nil, errors.New("interfaces unavailable") }
	}
	return cfg
}
