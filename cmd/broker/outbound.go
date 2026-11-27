package main

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/pairtask/pairtask/internal/peer/egress"
)

// outboundTransport is how a provider outside is reached: through
// connector's egress forwarder when the broker has one (a secured broker
// has no network of its own), else directly. TLS ends in the broker either
// way, so no credential passes through the forwarder in the clear.
func outboundTransport(cfg config) (http.RoundTripper, string) {
	if cfg.PairingEgressSocket == "" {
		return nil, "direct"
	}
	socket := cfg.PairingEgressSocket
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return egress.Dial(ctx, socket, address)
	}
	return transport, "egress " + socket
}

// pairbookHTTP keeps the directory client's direct-mode defaults and timeout.
func pairbookHTTP(cfg config) *http.Client {
	transport, _ := outboundTransport(cfg)
	if transport == nil {
		return nil
	}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second}
}
