//go:build linux

package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func serverControlReadNotice(t *testing.T, f messaging.Frames) {
	t.Helper()
	b, err := messaging.EncodePairingEpoch(messaging.PairingEpoch{})
	operatorNoError(t, err)
	operatorNoError(t, f.Send(b))
	b, err = f.Recv()
	operatorNoError(t, err)
	_, err = messaging.DecodePairingEpoch(b)
	operatorNoError(t, err)
	b, err = messaging.EncodeEnd()
	operatorNoError(t, err)
	operatorNoError(t, f.Send(b))
	b, err = f.Recv()
	operatorNoError(t, err)
	_, err = messaging.DecodeContactState(b)
	operatorNoError(t, err)
}
func (f *controlServerFixture) message(t *testing.T, ctx context.Context) *firstcontact.Conn {
	t.Helper()
	return f.pinnedSession(t, ctx, false)
}

func (f *controlServerFixture) pinnedSession(t *testing.T, ctx context.Context, control bool) *firstcontact.Conn {
	t.Helper()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", f.route.destination)
	operatorNoError(t, err)
	t.Cleanup(func() { raw.Close() })
	open := firstcontact.MessageClient
	if control {
		open = firstcontact.ControlClient
	}
	conn, err := open(ctx, raw, f.id, time.Now, f.route.pin)
	operatorNoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}
