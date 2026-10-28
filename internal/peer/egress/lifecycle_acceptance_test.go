package egress_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/egress"
)

func TestEgressAcceptanceCapacityIncludesIncompleteHeaders(t *testing.T) {
	cfg := routeConfig()
	cfg.MaxConns = 1
	entered := make(chan struct{})
	cfg.Resolver = resolveFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	path, cancel, _ := serveRoute(t, cfg)
	first := rawRoute(t, path)
	_, err := first.Write(routeHeader("peer.example:443"))
	routeOK(t, err)
	awaitRouteEvent(t, entered)
	second := rawRoute(t, path)
	// The second connection sends no header: the cap must act before parsing.
	assertRouteClosed(t, second)
	cancel()
}

func TestEgressAcceptanceCancellationClosesPartialHeader(t *testing.T) {
	path, cancel, _ := serveRoute(t, routeConfig())
	c := rawRoute(t, path)
	_, err := c.Write([]byte{1})
	routeOK(t, err)
	cancel()
	assertRouteClosed(t, c)
}

func TestEgressAcceptanceIncompleteHeaderHasItsOwnDeadline(t *testing.T) {
	path, _, _ := serveRoute(t, routeConfig())
	c := rawRoute(t, path)
	routeOK(t, c.SetDeadline(time.Now().Add(egress.HeaderTimeout+2*time.Second)))
	_, err := c.Write([]byte{1})
	routeOK(t, err)
	assertRouteClosed(t, c)
}

func TestEgressAcceptanceCancellationReachesResolverAndDial(t *testing.T) {
	for _, stage := range []string{"resolve", "dial"} {
		t.Run(stage, func(t *testing.T) { assertCancelledStage(t, stage) })
	}
}

func assertCancelledStage(t *testing.T, stage string) {
	t.Helper()
	cfg := routeConfig()
	entered, ended := make(chan struct{}), make(chan struct{})
	block := func(ctx context.Context) error { close(entered); <-ctx.Done(); close(ended); return ctx.Err() }
	if stage == "resolve" {
		cfg.Resolver = resolveFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) { return nil, block(ctx) })
	} else {
		cfg.Dial = func(ctx context.Context, _, _ string) (net.Conn, error) { return nil, block(ctx) }
	}
	path, cancel, _ := serveRoute(t, cfg)
	c := rawRoute(t, path)
	_, err := c.Write(routeHeader("peer.example:443"))
	routeOK(t, err)
	awaitRouteEvent(t, entered)
	cancel()
	awaitRouteEvent(t, ended)
}

func awaitRouteEvent(t *testing.T, event <-chan struct{}) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(time.Second):
		t.Fatal("route lifecycle event timed out")
	}
}

func TestEgressAcceptanceClientDeadlineClosesUnansweredHandshake(t *testing.T) {
	path, closed := fakeEgress(t, []byte{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	c, err := egress.Dial(ctx, path, "peer.example:443")
	if c != nil {
		c.Close()
		t.Fatal("unanswered handshake returned a tunnel")
	}
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("deadline not enforced: %v", err)
	}
	awaitRouteEvent(t, closed)
}

func TestEgressAcceptanceIdleTunnelCloses(t *testing.T) {
	cfg := routeConfig()
	cfg.IdleTimeout = 50 * time.Millisecond
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) { return echoRoute(t), nil }
	path, _, _ := serveRoute(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := egress.Dial(ctx, path, "peer.example:443")
	routeOK(t, err)
	defer c.Close()
	routeOK(t, c.SetDeadline(time.Now().Add(time.Second)))
	assertRouteClosed(t, c)
}

func TestEgressAcceptanceOutboundBytesAreBounded(t *testing.T) {
	cfg := routeConfig()
	cfg.MaxBytes = 4
	got := make(chan []byte, 1)
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { defer b.Close(); p, _ := io.ReadAll(b); got <- p }()
		return a, nil
	}
	path, _, _ := serveRoute(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := egress.Dial(ctx, path, "peer.example:443")
	routeOK(t, err)
	defer c.Close()
	routeOK(t, c.SetDeadline(time.Now().Add(time.Second)))
	c.Write([]byte("12345678"))
	select {
	case p := <-got:
		if string(p) != "1234" {
			t.Fatalf("outbound byte cap: %q", p)
		}
	case <-time.After(time.Second):
		t.Fatal("byte limit did not close target connection")
	}
}

func TestEgressAcceptanceInboundBytesAreBounded(t *testing.T) {
	cfg := routeConfig()
	cfg.MaxBytes = 4
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { defer b.Close(); b.Write([]byte("abcdefgh")) }()
		return a, nil
	}
	path, _, _ := serveRoute(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := egress.Dial(ctx, path, "peer.example:443")
	routeOK(t, err)
	defer c.Close()
	routeOK(t, c.SetDeadline(time.Now().Add(time.Second)))
	p, err := io.ReadAll(c)
	if err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if string(p) != "abcd" {
		t.Fatalf("inbound byte cap: %q", p)
	}
}
