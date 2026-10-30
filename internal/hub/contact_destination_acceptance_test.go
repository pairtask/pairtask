package hub

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

func moveDestination(t *testing.T, h *Hub, key string) {
	t.Helper()
	recordDestinationMove(t, h, key)
	h.PairingChanged()
}

func recordDestinationMove(t *testing.T, h *Hub, key string) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().Add(time.Hour)
	op := "moved-destination"
	_, err := h.store.BeginJoin(ctx, peer.JoinStart{OperationID: op, Joiner: h.identity.Fingerprint(), Destination: "new-peer:443", At: at})
	messageWorkerOK(t, err)
	_, err = h.store.SetCorrelation(ctx, op, peer.InviterHello{InvitationID: "inv-moved", OperationID: "remote-moved"}, at)
	messageWorkerOK(t, err)
	r, err := h.store.RecordVerified(ctx, op, peer.ExchangeResult{Peer: key, TranscriptID: make([]byte, 64)}, at)
	messageWorkerOK(t, err)
	_, err = h.store.RecordReceipt(ctx, op, peer.JoinReceipt{InvitationID: r.InvitationID, InviterOperationID: r.InviterOperationID, Joiner: r.Joiner, TranscriptID: r.TranscriptID}, at)
	messageWorkerOK(t, err)
	_, err = h.store.RecordDecision(ctx, op, peer.PairingAccepted, at)
	messageWorkerOK(t, err)
	_, err = h.store.RecordDecisionConfirmed(ctx, op, peer.PairingAccepted, at)
	messageWorkerOK(t, err)
}

func TestContactDestinationAcceptanceRedialsAndResetsFailuresOnce(t *testing.T) {
	h := newTestHub(t)
	var dialed []string
	h.SetPairing(PairingConfig{Clock: time.Now, Dial: func(_ context.Context, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, context.DeadlineExceeded
	}})
	messageSyncEqual(t, h.loadIdentity(context.Background()), "")
	h.msgs.init()
	key := messageWorkerContact(t, h, 1)
	h.syncContact(context.Background(), key)
	h.syncContact(context.Background(), key)
	messageSyncEqual(t, h.MessageSyncStatus(key).Failures, 2)
	moveDestination(t, h, key)
	h.syncContact(context.Background(), key)
	messageSyncEqual(t, dialed[len(dialed)-1], "new-peer:443")
	messageSyncEqual(t, h.MessageSyncStatus(key).Failures, 1)
	h.syncContact(context.Background(), key)
	messageSyncEqual(t, h.MessageSyncStatus(key).Failures, 2)
}

func TestContactDestinationAcceptanceStaleDialCannotRestoreFailure(t *testing.T) {
	h := newTestHub(t)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.SetPairing(PairingConfig{Clock: time.Now, Dial: func(ctx context.Context, _ string) (net.Conn, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, context.DeadlineExceeded
	}})
	messageSyncEqual(t, h.loadIdentity(context.Background()), "")
	h.msgs.init()
	key := messageWorkerContact(t, h, 1)
	go func() { defer close(done); h.syncContact(ctx, key) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("old-address dial never started")
	}
	moveDestination(t, h, key)
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stale dial never finished")
	}
	s := h.MessageSyncStatus(key)
	messageSyncEqual(t, s.Failures, 0)
	messageSyncEqual(t, s.LastFailure, "")
	messageSyncEqual(t, s.LastSuccess.IsZero(), true)
}

func TestContactDestinationAcceptanceMoveHaltsOnlyDialedLink(t *testing.T) {
	for _, serving := range []bool{false, true} {
		t.Run(map[bool]string{false: "dialer", true: "serving"}[serving], func(t *testing.T) {
			h := newTestHub(t)
			h.SetPairing(PairingConfig{Clock: time.Now})
			messageSyncEqual(t, h.loadIdentity(context.Background()), "")
			h.msgs.init()
			key := messageWorkerContact(t, h, 1)
			c, found, err := h.store.Contact(context.Background(), key)
			messageWorkerOK(t, err)
			messageSyncEqual(t, found, true)
			token, current := h.markDialed(c)
			messageSyncEqual(t, current, true)
			link, claimed := h.claimLive(key, serving)
			messageSyncEqual(t, claimed, true)
			link.token = token
			defer h.releaseLive(key, link)
			moveDestination(t, h, key)
			h.pair.workers.Wait()
			stopped := false
			select {
			case <-link.stop:
				stopped = true
			default:
			}
			messageSyncEqual(t, stopped, !serving)
			// An old snapshot cannot regain the new destination's generation.
			_, current = h.markDialed(c)
			messageSyncEqual(t, current, false)
			messageSyncEqual(t, h.recordDial(key, syncOutcome{token: token, failure: SyncTimeout}), false)
		})
	}
}

func TestContactDestinationAcceptanceMoveResetsLiveRetryBudget(t *testing.T) {
	h := newTestHub(t)
	h.SetPairing(PairingConfig{Clock: time.Now})
	messageSyncEqual(t, h.loadIdentity(context.Background()), "")
	h.msgs.init()
	key := messageWorkerContact(t, h, 1)
	c, _, err := h.store.Contact(context.Background(), key)
	messageWorkerOK(t, err)
	h.markDialed(c)
	for range 8 {
		h.liveDelay(key)
	}
	moveDestination(t, h, key)
	h.pair.workers.Wait()
	if delay := h.liveDelay(key); delay > time.Second {
		t.Fatalf("new destination inherited old live retry backoff: %s", delay)
	}
}

func TestContactDestinationAcceptanceUnchangedLiveLinkStillRecordsProgress(t *testing.T) {
	h := newTestHub(t)
	h.SetPairing(PairingConfig{Clock: time.Now})
	messageSyncEqual(t, h.loadIdentity(context.Background()), "")
	h.msgs.init()
	key := messageWorkerContact(t, h, 1)
	c, _, err := h.store.Contact(context.Background(), key)
	messageWorkerOK(t, err)
	token, _ := h.markDialed(c)
	link, claimed := h.claimLive(key, false)
	messageSyncEqual(t, claimed, true)
	link.token = token
	defer h.releaseLive(key, link)
	// Pairing notifications also happen for other contacts and invitations.
	h.PairingChanged()
	h.pair.workers.Wait()
	h.recordRound(link)
	if h.MessageSyncStatus(key).LastSuccess.IsZero() {
		t.Fatal("unrelated pairing change permanently silenced live progress")
	}
}

func TestContactDestinationAcceptanceRoundDetectingMoveStillHaltsOldLink(t *testing.T) {
	h := newTestHub(t)
	h.SetPairing(PairingConfig{Clock: time.Now})
	messageSyncEqual(t, h.loadIdentity(context.Background()), "")
	h.msgs.init()
	key := messageWorkerContact(t, h, 1)
	c, _, err := h.store.Contact(context.Background(), key)
	messageWorkerOK(t, err)
	token, _ := h.markDialed(c)
	link, claimed := h.claimLive(key, false)
	messageSyncEqual(t, claimed, true)
	link.token = token
	defer h.releaseLive(key, link)
	recordDestinationMove(t, h, key)
	// Arrange the valid ordering where a live round observes the new
	// pairing epoch before the asynchronous destination scan runs.
	h.msgs.mu.Lock()
	h.msgs.epoch++
	h.msgs.mu.Unlock()
	h.recordRound(link)
	h.checkDestinations()
	select {
	case <-link.stop:
	default:
		t.Fatal("live round adopted the move but left the old-address link open")
	}
}
