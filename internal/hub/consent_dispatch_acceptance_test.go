package hub

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
	"time"
)

type consentDispatchVersionAcceptor interface {
	AcceptVersion(context.Context, string, int, messaging.Message, time.Time) (messaging.Answer, error)
}
type consentDispatchLegacyAcceptor interface {
	Accept(context.Context, string, messaging.Message, time.Time) (messaging.Answer, error)
}

func consentDispatchAccept(t *testing.T, h *Hub, key string, m messaging.Message, now time.Time) messaging.Answer {
	t.Helper()
	var answer messaging.Answer
	var err error
	if s, ok := any(h.store.PeerMessages()).(consentDispatchVersionAcceptor); ok {
		answer, err = s.AcceptVersion(context.Background(), key, 8, m, now)
	} else {
		answer, err = any(h.store.PeerMessages()).(consentDispatchLegacyAcceptor).Accept(context.Background(), key, m, now)
	}
	messageWorkerOK(t, err)
	return answer
}

func consentDispatchFixture(t *testing.T, kind, ownerState string, isReview bool) (*Hub, *Client) {
	t.Helper()
	h, key := remoteHubFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	h.SetPairing(PairingConfig{Clock: func() time.Time { return now }})
	c := remoteHubClient(t, h, "recipient", 24)
	messageWorkerOK(t, h.store.SetConsentOptIn(ctx, key, true, 0, now))
	for _, op := range []string{protocol.ConsentMessage, protocol.ConsentReview, protocol.ConsentPublish} {
		messageWorkerOK(t, h.store.ResetConsentScope(ctx, key, c.ID, op, 0, now))
	}
	m := messaging.Message{ID: "queued-consent-dispatch", N: 1, From: "claimed-sender", To: []string{c.ID}, CreatedAt: now, Text: "immutable held body"}
	if isReview {
		m.Review = &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "293", Base: "base-293", Head: "head-293", Publish: true, Formal: true}
	}
	a := consentDispatchAccept(t, h, key, m, now)
	if !a.Held {
		t.Fatalf("request was not held: %+v", a)
	}
	page, err := h.store.ConsentPage(ctx, key, 0, 10)
	messageWorkerOK(t, err)
	if len(page.Items) != 1 {
		t.Fatalf("held page: %+v", page)
	}
	v := page.Items[0]
	_, err = h.store.DecideConsent(ctx, store.ConsentDecision{ID: v.ID, Digest: v.Digest, Revision: v.Revision, Choice: kind, Evidence: store.ConsentEvidence{Kind: kind, Epoch: h.epoch, Gen: 7}, Now: now})
	messageWorkerOK(t, err)
	// Durable evidence is restored before the broker loop starts. A matching
	// epoch/gen without a live owner must not authorize dispatch.
	h.gen = 7
	if ownerState != "absent" {
		h.owner = NewOperatorSession(1)
		h.owner.gen = 7
		if ownerState == "replacement" {
			h.gen = 8
			h.owner.gen = 8
		}
	}
	return h, c
}

func consentDispatchPass(t *testing.T, h *Hub, c *Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	defer func() { cancel(); <-h.Done() }()
	response := h.submit(ctx, operatorRequest{ctx: ctx, kind: reqFinish, finish: func(runCtx context.Context) protocol.Message {
		h.drainImports(runCtx, map[string]*Client{c.ID: c})
		return protocol.Message{Type: protocol.TypeAck}
	}})
	messageSyncEqual(t, response.Type, protocol.TypeAck)
}

func TestConsentDispatchAcceptanceSessionNeedsCurrentLiveOwner(t *testing.T) {
	for _, isReview := range []bool{false, true} {
		for _, owner := range []string{"absent", "replacement"} {
			name := owner
			if isReview {
				name += "-review"
			}
			t.Run(name, func(t *testing.T) {
				h, c := consentDispatchFixture(t, protocol.ConsentSession, owner, isReview)
				consentDispatchPass(t, h, c)
				remoteHubEmpty(t, c)
				rows, err := h.store.ClaimPendingFor(context.Background(), protocol.Recipient{AgentID: c.ID, SessionID: "inspection-session"}, 24)
				messageWorkerOK(t, err)
				messageSyncEqual(t, len(rows), 0)
				pending, err := h.store.PendingImports(context.Background(), 20)
				messageWorkerOK(t, err)
				messageSyncEqual(t, len(pending), 0)
				reviews, total, err := h.store.Reviews(context.Background(), c.ID, 20)
				messageWorkerOK(t, err)
				messageSyncEqual(t, total, 0)
				messageSyncEqual(t, len(reviews), 0)
			})
		}
	}
}

func TestConsentDispatchAcceptanceOnceAndLiveSessionDispatchExactlyOnce(t *testing.T) {
	for _, kind := range []string{protocol.ConsentOnce, protocol.ConsentSession} {
		t.Run(kind, func(t *testing.T) {
			owner := "live"
			if kind == protocol.ConsentOnce {
				owner = "absent"
			}
			h, c := consentDispatchFixture(t, kind, owner, false)
			consentDispatchPass(t, h, c)
			select {
			case delivered := <-c.Send:
				messageSyncEqual(t, delivered.Origin, protocol.OriginRemote)
			default:
			}
			remoteHubEmpty(t, c)
			rows, err := h.store.ClaimPendingFor(context.Background(), protocol.Recipient{AgentID: c.ID, SessionID: "positive-inspection"}, 24)
			messageWorkerOK(t, err)
			messageSyncEqual(t, len(rows), 1)
			pending, err := h.store.PendingImports(context.Background(), 20)
			messageWorkerOK(t, err)
			messageSyncEqual(t, len(pending), 0)
		})
	}
}
