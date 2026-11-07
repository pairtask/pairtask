package firstcontact

import (
	"context"
	"net"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// ControlClient dials a control session pinned to pin (compile-only seam:
// delegates to MessageClient; #276).
func ControlClient(ctx context.Context, raw net.Conn, id *peer.WorkspaceIdentity, clock func() time.Time, pin string) (*Conn, error) {
	return MessageClient(ctx, raw, id, clock, pin)
}
