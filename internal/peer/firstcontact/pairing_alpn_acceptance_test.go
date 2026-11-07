//go:build unix

package firstcontact_test

import (
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
)

type pairingTLSOptions struct{ serverProtocols, clientProtocols []string }
type pairingTLSConnections struct {
	server, client *firstcontact.Conn
	alice, bob     *peer.WorkspaceIdentity
}

func pairingProtocolEndpoints(t *testing.T, opts pairingTLSOptions) pairingTLSConnections {
	t.Helper()
	endpoints := pairingTLSConnections{alice: identity(t), bob: identity(t)}
	return endpoints.reconnect(t, opts)
}

func (c pairingTLSConnections) reconnect(t *testing.T, opts pairingTLSOptions) pairingTLSConnections {
	t.Helper()
	ctx := testContext(t)
	a, b := pipe(t)
	result := make(chan connResult, 1)
	go func() {
		conn, e := firstcontact.ServerWith(ctx, a, c.alice, time.Now, firstcontact.ServerOptions{PairingProtocols: opts.serverProtocols})
		result <- connResult{conn, e}
	}()
	client, err := firstcontact.ClientWith(ctx, b, c.bob, time.Now, firstcontact.ClientOptions{PairingProtocols: opts.clientProtocols})
	mustOK(t, err)
	server := <-result
	mustOK(t, server.err)
	return pairingTLSConnections{server.conn, client, c.alice, c.bob}
}

func pairingProtocolConnection(t *testing.T, serverProtocols, clientProtocols []string) (*firstcontact.Conn, *firstcontact.Conn) {
	t.Helper()
	c := pairingProtocolEndpoints(t, pairingTLSOptions{serverProtocols, clientProtocols})
	return c.server, c.client
}

func TestPairingALPNAcceptanceDefaultsNegotiateEpochMode(t *testing.T) {
	server, client := connected(t, identity(t), identity(t))
	equal(t, server.Protocol(), firstcontact.PairingALPN3)
	equal(t, client.Protocol(), firstcontact.PairingALPN3)
	equal(t, server.ExchangeMode(), peer.ModeEpoch)
	equal(t, client.ExchangeMode(), peer.ModeEpoch)
	equal(t, server.Connection().Mode.Stored(), peer.ModeEpoch)
	equal(t, client.Connection().Mode.Stored(), peer.ModeEpoch)
}

func TestPairingALPNAcceptanceServerPreferenceWinsReversedClient(t *testing.T) {
	server, client := pairingProtocolConnection(t, nil, []string{firstcontact.ALPN, firstcontact.PairingALPN3})
	equal(t, server.Protocol(), firstcontact.PairingALPN3)
	equal(t, client.Protocol(), firstcontact.PairingALPN3)
}

func TestPairingALPNAcceptanceV8ClientKeepsLegacyMode(t *testing.T) {
	server, client := pairingProtocolConnection(t, nil, []string{firstcontact.ALPN})
	equal(t, server.Protocol(), firstcontact.ALPN)
	equal(t, client.Protocol(), firstcontact.ALPN)
	equal(t, server.ExchangeMode(), peer.ModeLegacy)
	equal(t, client.ExchangeMode(), peer.ModeLegacy)
	equal(t, server.Connection().Mode.Stored(), peer.ModeLegacy)
	equal(t, client.Connection().Mode.Stored(), peer.ModeLegacy)
}

func TestPairingALPNAcceptanceV8ServerKeepsLegacyMode(t *testing.T) {
	server, client := pairingProtocolConnection(t, []string{firstcontact.ALPN}, nil)
	equal(t, server.Protocol(), firstcontact.ALPN)
	equal(t, client.Protocol(), firstcontact.ALPN)
	equal(t, server.ExchangeMode(), peer.ModeLegacy)
	equal(t, client.ExchangeMode(), peer.ModeLegacy)
	equal(t, server.Connection().Mode.Stored(), peer.ModeLegacy)
	equal(t, client.Connection().Mode.Stored(), peer.ModeLegacy)
}
