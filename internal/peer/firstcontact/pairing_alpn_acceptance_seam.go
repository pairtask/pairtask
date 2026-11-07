package firstcontact

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer"
	"net"
	"time"
)

const PairingALPN3 = "pairtask-pairing/3"

type ClientOptions struct{ PairingProtocols []string }

// Legacy delegate; protocol preferences remain unchanged.
func ClientWith(ctx context.Context, raw net.Conn, id *peer.WorkspaceIdentity, clock func() time.Time, opts ClientOptions) (*Conn, error) {
	return Client(ctx, raw, id, clock)
}
func (c *Conn) ExchangeMode() peer.ExchangeMode {
	if c.protocol == PairingALPN3 {
		return peer.ModeEpoch
	}
	if c.protocol == ALPN {
		return peer.ModeLegacy
	}
	return ""
}
