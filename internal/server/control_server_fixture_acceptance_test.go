//go:build linux

package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

type controlServerFixture struct {
	f      *redeemOperatorFixture
	id     *peer.WorkspaceIdentity
	remote *store.Store
	route  messageRoute
	now    *atomic.Int64
}

func newControlServerFixture(t *testing.T) *controlServerFixture {
	t.Helper()
	now := &atomic.Int64{}
	now.Store(time.Now().Unix())
	cfg := redeemConfig()
	cfg.Clock = func() time.Time { return time.Unix(now.Load(), 0) }
	cfg.MessageTick = make(chan time.Time)
	f := newRedeemOperator(t, cfg, "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	ep, err := f.h.PairingEndpoint()
	operatorNoError(t, err)
	bound, err := f.st.PeerMessages().Admit(context.Background(), id.Fingerprint())
	operatorNoError(t, err)
	operatorNoError(t, bound.SawVersion(context.Background(), 9))
	remote := pauseServerRemoteStore(t, ep.Identity.Fingerprint())
	return &controlServerFixture{f: f, id: id, remote: remote, route: messageRoute{relayToPairing(t, f), ep.Identity.Fingerprint()}, now: now}
}
func (f *controlServerFixture) control(t *testing.T, ctx context.Context) *firstcontact.Conn {
	t.Helper()
	return f.pinnedSession(t, ctx, true)
}
func (f *controlServerFixture) awaitNotice(t *testing.T, state string, attempts int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var got string
		var count int
		err := f.f.db.QueryRow(`SELECT state,attempts FROM contact_notices_out WHERE contact=? ORDER BY gen DESC LIMIT 1`, f.id.Fingerprint()).Scan(&got, &count)
		matches := err == nil && got == state && count == attempts
		if matches {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("notice state=%q attempts=%d err=%v; want %q/%d", got, count, err, state, attempts)
		}
		time.Sleep(time.Millisecond)
	}
}
func (f *controlServerFixture) remoteControl(t *testing.T, ctx context.Context) messaging.ControlSession {
	t.Helper()
	conn := f.control(t, ctx)
	bound, err := f.remote.PeerMessages().AdmitControl(ctx, f.route.pin)
	operatorNoError(t, err)
	return messaging.ControlSession{Frames: conn.MessageFrames(ctx), Store: bound, Contact: f.route.pin}
}
