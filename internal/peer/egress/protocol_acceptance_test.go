package egress_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/egress"
)

func TestEgressAcceptanceMalformedHeadersNeverDial(t *testing.T) {
	for _, frame := range [][]byte{{9, 0, 1, 'x'}, {1, 0, 0}, {1, 1, 0}, {1}, {1, 0, 9, 'x'}} {
		t.Run(string(frame), func(t *testing.T) {
			cfg := routeConfig()
			var calls atomic.Int32
			cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
				calls.Add(1)
				return nil, errors.New("must not dial")
			}
			path, _, _ := serveRoute(t, cfg)
			c := rawRoute(t, path)
			_, err := c.Write(frame)
			routeOK(t, err)
			routeOK(t, c.(*net.UnixConn).CloseWrite())
			var reply [1]byte
			n, err := c.Read(reply[:])
			if n == 1 && reply[0] == egress.StatusConnected {
				t.Fatal("malformed request connected")
			}
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				t.Fatal("malformed request hung")
			}
			if calls.Load() != 0 {
				t.Fatal("malformed request dialed")
			}
		})
	}
}

func TestEgressAcceptanceOversizedPrefixRefusedWithoutBody(t *testing.T) {
	path, _, _ := serveRoute(t, routeConfig())
	c := rawRoute(t, path)
	_, err := c.Write([]byte{1, 255, 255})
	routeOK(t, err)
	// Keep the write side open: the length alone must be enough to refuse.
	var reply [1]byte
	_, err = c.Read(reply[:])
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("server waited for oversized body")
	}
	if err == nil && reply[0] == egress.StatusConnected {
		t.Fatal("oversized request connected")
	}
}

func TestEgressAcceptanceInvalidDestinationRefusedBeforeResolution(t *testing.T) {
	for _, destination := range []string{"host", ":443", "host:0", "host:65536", "host:abc", "ho\nst:443", strings.Repeat("x", 256)} {
		t.Run(destination, func(t *testing.T) {
			cfg := routeConfig()
			var lookups atomic.Int32
			original := cfg.Resolver
			cfg.Resolver = resolveFunc(func(ctx context.Context, network, host string) ([]netip.Addr, error) {
				lookups.Add(1)
				return original.LookupNetIP(ctx, network, host)
			})
			path, _, _ := serveRoute(t, cfg)
			if routeStatus(t, path, destination) != egress.StatusRefused {
				t.Fatal("bad destination not refused")
			}
			if lookups.Load() != 0 {
				t.Fatal("invalid destination reached DNS")
			}
		})
	}
}

func TestEgressAcceptanceClientMapsStatusAndClosesFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response []byte
		want     error
	}{
		{"refused", []byte{1}, egress.ErrRefused},
		{"dial failed", []byte{2}, egress.ErrDialFailed},
		{"unknown", []byte{99}, egress.ErrProtocol},
		{"truncated", nil, egress.ErrProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, closed := fakeEgress(t, tc.response)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err := egress.Dial(ctx, path, "peer.example:443")
			if conn != nil {
				conn.Close()
				t.Fatal("failure returned a usable connection")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("client left rejected connection open")
			}
		})
	}
}

func fakeEgress(t *testing.T, response []byte) (string, <-chan struct{}) {
	t.Helper()
	path := routeSocket(t)
	ln, err := net.Listen("unix", path)
	routeOK(t, err)
	t.Cleanup(func() { ln.Close() })
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(2 * time.Second))
		var header [3]byte
		if _, err := io.ReadFull(c, header[:]); err != nil {
			return
		}
		io.CopyN(io.Discard, c, int64(binary.BigEndian.Uint16(header[1:])))
		if response == nil {
			return
		}
		c.Write(response)
		var b [1]byte
		c.Read(b[:])
	}()
	return path, closed
}

func TestEgressAcceptanceDialFailureStatus(t *testing.T) {
	path, _, _ := serveRoute(t, routeConfig())
	if got := routeStatus(t, path, "peer.example:443"); got != egress.StatusDialFailed {
		t.Fatalf("status=%d", got)
	}
}
