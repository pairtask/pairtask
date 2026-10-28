package hub

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

func messageWorkerContact(t *testing.T, h *Hub, n int) string {
	t.Helper()
	ctx := context.Background()
	at := time.Now()
	key := fmt.Sprintf("sha256:%064x", n)
	op := fmt.Sprint("join-", n)
	_, err := h.store.BeginJoin(ctx, peer.JoinStart{OperationID: op, Joiner: h.identity.Fingerprint(), Name: "Local", Destination: fmt.Sprintf("peer-%d:443", n), At: at})
	messageWorkerOK(t, err)
	_, err = h.store.SetCorrelation(ctx, op, peer.InviterHello{InvitationID: "inv-" + op, OperationID: "remote-" + op}, at)
	messageWorkerOK(t, err)
	row, err := h.store.RecordVerified(ctx, op, peer.ExchangeResult{Peer: key, TranscriptID: make([]byte, 64)}, at)
	messageWorkerOK(t, err)
	_, err = h.store.RecordReceipt(ctx, op, peer.JoinReceipt{InvitationID: row.InvitationID, InviterOperationID: row.InviterOperationID, Joiner: row.Joiner, TranscriptID: row.TranscriptID}, at)
	messageWorkerOK(t, err)
	_, err = h.store.RecordDecision(ctx, op, peer.PairingAccepted, at)
	messageWorkerOK(t, err)
	_, err = h.store.RecordDecisionConfirmed(ctx, op, peer.PairingAccepted, at)
	messageWorkerOK(t, err)
	return key
}
func messageWorkerOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func awaitMessageWorkerDial(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case destination := <-started:
		return destination
	case <-time.After(2 * time.Second):
		t.Fatal("message worker did not dial")
		return ""
	}
}

type blockedMessageDials struct {
	started chan string
	release chan struct{}
	active  atomic.Int32
}

func (d *blockedMessageDials) dial(ctx context.Context, destination string) (net.Conn, error) {
	d.active.Add(1)
	defer d.active.Add(-1)
	d.started <- destination
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-d.release:
		return nil, context.Canceled
	}
}
func (d *blockedMessageDials) requireNoDial(t *testing.T) {
	t.Helper()
	select {
	case destination := <-d.started:
		t.Fatalf("fifth dial started early: %s", destination)
	default:
	}
}
func awaitMessageSlot(t *testing.T, pool *syncPool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(pool.slots) == 4 {
		if time.Now().After(deadline) {
			t.Fatal("completed dial did not release slot")
		}
		time.Sleep(time.Millisecond)
	}
}
func fiveMessageContacts(t *testing.T, h *Hub) []string {
	t.Helper()
	keys := make([]string, 5)
	for i := range keys {
		keys[i] = messageWorkerContact(t, h, i+1)
	}
	return keys
}
func TestMessageWorkerAcceptanceBoundedParallelDialsAndDeferredWake(t *testing.T) {
	h := newTestHub(t)
	d := &blockedMessageDials{started: make(chan string, 8), release: make(chan struct{})}
	h.SetPairing(PairingConfig{Clock: time.Now, Dial: d.dial})
	messageSyncEqual(t, h.loadIdentity(context.Background()), "")
	h.msgs.init()
	keys := fiveMessageContacts(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	pool := syncPool{slots: make(chan struct{}, 4)}
	defer func() { cancel(); pool.wg.Wait() }()
	for i := 0; i < 4; i++ {
		h.spawnSync(ctx, &pool, keys[i])
		awaitMessageWorkerDial(t, d.started)
	}
	messageSyncEqual(t, d.active.Load(), int32(4))
	h.spawnSync(ctx, &pool, keys[4])
	messageSyncEqual(t, len(pool.slots), 4)
	messageSyncEqual(t, d.active.Load(), int32(4))
	d.requireNoDial(t)
	d.release <- struct{}{}
	awaitMessageSlot(t, &pool)
	h.spawnSync(ctx, &pool, keys[4])
	messageSyncEqual(t, awaitMessageWorkerDial(t, d.started), "peer-5:443")
}

func TestMessageWorkerAcceptanceStartTickBackoffAndExplicitWake(t *testing.T) {
	h := newTestHub(t)
	tick := make(chan time.Time)
	started := make(chan string, 8)
	var seconds atomic.Int64
	seconds.Store(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC).Unix())
	h.SetPairing(PairingConfig{Clock: func() time.Time { return time.Unix(seconds.Load(), 0) }, MessageTick: tick,
		Dial: func(ctx context.Context, destination string) (net.Conn, error) {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 30*time.Second {
				return nil, fmt.Errorf("message dial lacks 30-second budget")
			}
			started <- destination
			return nil, context.DeadlineExceeded
		}})
	if problem := h.loadIdentity(context.Background()); problem != "" {
		t.Fatal(problem)
	}
	key := messageWorkerContact(t, h, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); h.pair.workers.Wait() }()
	h.startMessageWorker(ctx)
	awaitMessageWorkerDial(t, started)
	awaitMessageFailures(t, h, key, 1)
	tick <- time.Unix(seconds.Load(), 0)
	// An explicit wake bypasses backoff; the prior same-time tick must not
	// create an extra attempt. Both are handled by the same worker loop.
	h.SyncMessages(key)
	awaitMessageWorkerDial(t, started)
	awaitMessageFailures(t, h, key, 2)
	seconds.Add(10 * 60)
	tick <- time.Unix(seconds.Load(), 0)
	awaitMessageWorkerDial(t, started)
	awaitMessageFailures(t, h, key, 3)
	select {
	case extra := <-started:
		t.Fatalf("unexpected extra dial %s", extra)
	default:
	}
}

func awaitMessageFailures(t *testing.T, h *Hub, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status := h.MessageSyncStatus(key)
		if status.Failures == want {
			if status.LastFailure != SyncTimeout {
				t.Fatalf("wrong failure class %+v", status)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("wanted %d failures, status %+v", want, status)
		}
		time.Sleep(time.Millisecond)
	}
}
