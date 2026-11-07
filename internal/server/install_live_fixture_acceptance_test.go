//go:build linux

package server

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
)

type installLiveFixture struct {
	f      *redeemOperatorFixture
	remote *peer.WorkspaceIdentity
	ep     hub.PairingEndpoint
}

func newInstallLiveFixture(t *testing.T) *installLiveFixture {
	t.Helper()
	cfg := redeemConfig()
	cfg.MessageTick = make(chan time.Time)
	cfg.SettleTick = make(chan time.Time)
	f := newRedeemOperator(t, cfg, "")
	remote := messageIdentity(t)
	ep, err := f.h.PairingEndpoint()
	operatorNoError(t, err)
	x := &installLiveFixture{f: f, remote: remote, ep: ep}
	o := x.outcome(t, "initial-live-pair", 11)
	_, err = f.st.Commit(context.Background(), o)
	operatorNoError(t, err)
	_, err = f.st.Accept(context.Background(), remote.Fingerprint())
	operatorNoError(t, err)
	_, err = f.st.DeliverExchange(context.Background(), peer.ExchangeDelivery{IdentityKey: remote.Fingerprint(), OperationID: o.OperationID, Decision: peer.PairingAccepted, At: time.Now()})
	operatorNoError(t, err)
	return x
}
func (f *installLiveFixture) outcome(t *testing.T, id string, fill byte) peer.PairingOutcome {
	t.Helper()
	mark, err := f.f.st.PairingMark(context.Background())
	operatorNoError(t, err)
	return peer.PairingOutcome{OperationID: id, InvitationID: "inv-" + id, IdentityKey: f.remote.Fingerprint(), Name: "Peer", Mode: peer.ModeEpoch, BegunMark: mark, TranscriptID: bytes.Repeat([]byte{fill}, 64), At: time.Now()}
}

type installIdleFrames struct {
	ctx   context.Context
	ready chan<- struct{}
}

func (f installIdleFrames) Send([]byte) error { return f.ctx.Err() }
func (f installIdleFrames) Recv() ([]byte, error) {
	select {
	case f.ready <- struct{}{}:
	default:
	}
	<-f.ctx.Done()
	return nil, f.ctx.Err()
}
func (f *installLiveFixture) live(t *testing.T) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	claim, ok := f.ep.ClaimLink(f.remote.Fingerprint())
	operatorEqual(t, "claim live", ok, true)
	bound, err := f.f.st.PeerMessages().Admit(ctx, f.remote.Fingerprint())
	operatorNoError(t, err)
	done := make(chan struct{})
	ready := make(chan struct{}, 1)
	go func() {
		defer close(done)
		_ = claim.Run(ctx, func(run context.Context) messaging.Frames { return installIdleFrames{run, ready} }, hub.FirstRound{Store: bound, Version: 9})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("fixture live link did not stop")
		}
	})
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture live link did not reach idle read")
	}
	return done
}
func installLiveClosed(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("install left the old live socket idle instead of closing it")
	}
}
