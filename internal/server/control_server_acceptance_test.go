//go:build linux

package server

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestControlServerAcceptancePausedNoAddressDeliversAndCountsOnce(t *testing.T) {
	f := newControlServerFixture(t)
	pauseServerContact(t, f.f, f.id.Fingerprint(), true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := f.remoteControl(t, ctx).Dial(ctx)
	operatorNoError(t, err)
	operatorEqual(t, "received paused notice", result.Received, true)
	f.awaitNotice(t, "delivered", 1)
	var state string
	operatorNoError(t, f.remoteInspection(t).QueryRow(`SELECT peer_state FROM peer_contacts WHERE identity_key=?`, f.route.pin).Scan(&state))
	operatorEqual(t, "authenticated remote pause", state, "paused")
	operatorEqual(t, "no imported messages", f.f.count("peer_messages"), 0)
}
func TestControlServerAcceptanceUnpairedRetainedContactStillServes(t *testing.T) {
	f := newControlServerFixture(t)
	f.unpair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := f.remoteControl(t, ctx).Dial(ctx)
	operatorNoError(t, err)
	operatorEqual(t, "received terminal notice", result.ClosedByUnpair, true)
	f.awaitNotice(t, "delivered", 1)
	_, err = f.remote.PeerMessages().Admit(context.Background(), f.route.pin)
	operatorEqual(t, "remote traffic revoked", errors.Is(err, store.ErrUnpairedByThem), true)
	operatorEqual(t, "no imports through control", f.f.count("remote_imports"), 0)
}
func TestControlServerAcceptanceMessageClaimExcludesControl(t *testing.T) {
	f := newControlServerFixture(t)
	ep, err := f.f.h.PairingEndpoint()
	operatorNoError(t, err)
	end, ok := ep.BeginMessages(f.id.Fingerprint())
	operatorEqual(t, "fixture acquired claim", ok, true)
	defer end(false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := f.remoteControl(t, ctx)
	_, err = s.Dial(ctx)
	if err == nil {
		t.Fatal("control session ran concurrently with the same contact's message claim")
	}
}
func TestControlServerAcceptanceRemoteUnpairBlocksOrdinaryTLS(t *testing.T) {
	f := newControlServerFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	bound, err := f.remote.PeerMessages().Admit(ctx, f.route.pin)
	operatorNoError(t, err)
	operatorNoError(t, bound.SawVersion(ctx, 9))
	f.remoteUnpair(t)
	_, err = f.remoteControl(t, ctx).Dial(ctx)
	// A terminal receiver acknowledges and closes without another notice.
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	_, err = messageConnect(t, f.route, f.id)
	if err == nil {
		t.Fatal("remote-unpaired contact admitted ordinary message TLS")
	}
	reply := f.f.h.SubmitOperator(context.Background(), 0, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend, ContactKey: f.id.Fingerprint(), ContactText: "refused", MessageID: "remote-unpaired-send"})
	operatorEqual(t, "operator sees remote unpair", reply.Code, "contact_unpaired_by_them")
}

func TestControlServerAcceptanceLostAckCountsBeforeSuccessfulRetry(t *testing.T) {
	f := newControlServerFixture(t)
	pauseServerContact(t, f.f, f.id.Fingerprint(), true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := f.control(t, ctx)
	frames := conn.MessageFrames(ctx)
	serverControlReadNotice(t, frames)
	operatorNoError(t, conn.Close())
	f.awaitNotice(t, "pending", 1)
	f.now.Add(61)
	_, err := f.remoteControl(t, ctx).Dial(ctx)
	operatorNoError(t, err)
	f.awaitNotice(t, "delivered", 2)
}

func TestControlServerAcceptanceIncomingReplacementStillRespectsMinuteBound(t *testing.T) {
	f := newControlServerFixture(t)
	pauseServerContact(t, f.f, f.id.Fingerprint(), true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := f.remoteControl(t, ctx).Dial(ctx)
	operatorNoError(t, err)
	f.awaitNotice(t, "delivered", 1)
	pauseServerContact(t, f.f, f.id.Fingerprint(), false)
	_, err = f.remoteControl(t, ctx).Dial(ctx)
	if err == nil {
		t.Fatal("second incoming control session bypassed the contact's minute bound")
	}
	f.awaitNotice(t, "pending", 0)
	f.now.Add(61)
	_, err = f.remoteControl(t, ctx).Dial(ctx)
	operatorNoError(t, err)
	f.awaitNotice(t, "delivered", 1)
}
