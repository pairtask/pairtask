package hub

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func hubPauseContact(t *testing.T, h *Hub, key string, paused bool) {
	t.Helper()
	op := "resume"
	if paused {
		op = "pause"
	}
	id := op + "-" + key
	audit, err := h.store.RecordOperatorAudit(context.Background(), store.OperatorAudit{Action: "contact " + op, MessageID: id, Scope: key, Result: store.AuditPending})
	messageWorkerOK(t, err)
	_, err = h.store.SetContactPaused(context.Background(), store.ContactPause{OperationID: id, Contact: key, Paused: paused, AuditID: audit, At: time.Now()})
	messageWorkerOK(t, err)
}

func TestContactPauseHubAcceptanceWorkspacesAndSnapshotReflectLocalPause(t *testing.T) {
	f := newAddressFixture(t)
	f.joined(t)
	h, key := f.h, f.key
	a := remoteHubClient(t, h, "sender", 15)
	remoteHubShare(t, h, key, a.ID)
	for _, paused := range []bool{true, false} {
		hubPauseContact(t, h, key, paused)
		out := h.workspaces(context.Background(), Inbound{From: a, Msg: protocol.Message{Type: protocol.TypeWorkspacesGet, RequestID: "discover"}})
		want := 1
		if paused {
			want = 0
		}
		messageSyncEqual(t, len(out.Workspaces), want)
		_, err := h.store.ResolveWorkspace(context.Background(), key)
		messageSyncEqual(t, errors.Is(err, store.ErrNoWorkspace), paused)
		snapshot, err := h.pairingSnapshot(context.Background())
		messageWorkerOK(t, err)
		messageSyncEqual(t, len(snapshot.Contacts), 1)
		messageSyncEqual(t, snapshot.Contacts[0].PausedAt != nil, paused)
	}
}

func TestContactPauseHubAcceptancePauseStopsEitherLiveDirectionAndRefusesNewSend(t *testing.T) {
	for _, inbound := range []bool{true, false} {
		t.Run(map[bool]string{true: "inbound", false: "outbound"}[inbound], func(t *testing.T) {
			f := newAddressFixture(t)
			f.joined(t)
			f.run(t, func(context.Context, string) (net.Conn, error) { return nil, context.Canceled })
			link, ok := f.h.claimLive(f.key, inbound)
			messageSyncEqual(t, ok, true)
			defer f.h.releaseLive(f.key, link)
			out := addressAwait(t, f.submit(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: "pause", ContactKey: f.key, MessageID: "pause"}))
			messageSyncEqual(t, out.Type, protocol.TypeOperatorContactResult)
			select {
			case <-link.stop:
			default:
				t.Fatal("pause did not stop live link")
			}
			out = addressAwait(t, f.submit(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend, ContactKey: f.key, ContactText: "blocked", MessageID: "blocked-send"}))
			messageSyncEqual(t, out.Code, "contact_paused")
			rows, _, err := f.h.store.PeerMessages().Page(context.Background(), f.key, 0, 10)
			messageWorkerOK(t, err)
			messageSyncEqual(t, len(rows), 0)
		})
	}
}

func TestContactPauseHubAcceptanceResumeWakesDialAndPendingImport(t *testing.T) {
	f := newAddressFixture(t)
	f.joined(t)
	ctx := context.Background()
	remoteHubClient(t, f.h, "worker", 15)
	remoteHubClient(t, f.h, "reviewer", 15)
	remoteHubShare(t, f.h, f.key, "worker")
	_, err := f.h.store.PeerMessages().Accept(ctx, f.key, messaging.Message{ID: "pending-import", N: 1, CreatedAt: time.Now(), Text: "held", To: []string{"worker"}}, time.Now())
	messageWorkerOK(t, err)
	_, err = f.h.store.PeerMessages().SetReviewGrant(ctx, f.key, store.ReviewGrantChange{Agents: []string{"reviewer"}, Mode: store.ReviewAuto, Now: time.Now()})
	messageWorkerOK(t, err)
	_, err = f.h.store.PeerMessages().Accept(ctx, f.key, messaging.Message{ID: "pending-review", N: 2, CreatedAt: time.Now(), Text: "review after resume", To: []string{"reviewer"}, Review: &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "276", Base: "base-pin", Head: "head-pin"}}, time.Now())
	messageWorkerOK(t, err)
	hubPauseContact(t, f.h, f.key, true)
	dials := make(chan string, 8)
	f.run(t, func(_ context.Context, to string) (net.Conn, error) { dials <- to; return nil, context.Canceled })
	f.h.startMessageWorker(f.ctx)
	select {
	case <-dials:
		t.Fatal("paused contact dialed at worker startup")
	case <-time.After(30 * time.Millisecond):
	}
	out := addressAwait(t, f.submit(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: "resume", ContactKey: f.key, MessageID: "resume-wakeup"}))
	messageSyncEqual(t, out.Type, protocol.TypeOperatorContactResult)
	awaitMessageWorkerDial(t, dials)
	deadline := time.Now().Add(2 * time.Second)
	for {
		pending, err := f.h.store.PendingImports(ctx, 10)
		messageWorkerOK(t, err)
		if len(pending) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resume never drained pending imports")
		}
		time.Sleep(time.Millisecond)
	}
	reviews, _, err := f.h.store.Reviews(ctx, "reviewer", 10)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(reviews), 1)
}

func TestContactPauseHubAcceptanceOldPauseReplayNeverStopsFreshLiveLink(t *testing.T) {
	for _, inbound := range []bool{true, false} {
		t.Run(map[bool]string{true: "inbound", false: "outbound"}[inbound], func(t *testing.T) {
			f := newAddressFixture(t)
			f.joined(t)
			f.run(t, func(context.Context, string) (net.Conn, error) { return nil, context.Canceled })
			pause := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: "pause", ContactKey: f.key, MessageID: "original-pause"}
			out := addressAwait(t, f.submit(pause))
			messageSyncEqual(t, out.Type, protocol.TypeOperatorContactResult)
			out = addressAwait(t, f.submit(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: "resume", ContactKey: f.key, MessageID: "intervening-resume"}))
			messageSyncEqual(t, out.Type, protocol.TypeOperatorContactResult)
			fresh, err := f.h.store.PeerMessages().Admit(context.Background(), f.key)
			messageWorkerOK(t, err)
			_, generation, _ := fresh.Fence()
			link, ok := f.h.claimLive(f.key, inbound)
			messageSyncEqual(t, ok, true)
			defer f.h.releaseLive(f.key, link)
			out = addressAwait(t, f.submit(pause))
			messageSyncEqual(t, out.Type, protocol.TypeOperatorContactResult)
			select {
			case <-link.stop:
				t.Fatal("replaying old pause halted a fresh live link")
			default:
			}
			messageSyncEqual(t, f.h.Live(f.key), true)
			c := f.contact(t)
			messageSyncEqual(t, c.PausedAt.IsZero(), true)
			messageSyncEqual(t, c.Generation, generation)
			_, err = fresh.Offer(context.Background(), f.key, time.Now(), messaging.SessionLimit)
			messageWorkerOK(t, err)
		})
	}
}
