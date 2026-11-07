package bridge

import (
	"context"
	"fmt"
	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

type consentBridgeClient struct {
	h      *hub.Hub
	agent  *hub.Client
	forges forge.Registry
	next   int
}

func (c *consentBridgeClient) ReviewProtocol() int { return 24 }

func (c *consentBridgeClient) ReviewForges(context.Context) (forge.Registry, error) {
	return c.forges, nil
}
func (c *consentBridgeClient) ReviewCall(ctx context.Context, m protocol.Message) (protocol.Message, error) {
	c.next++
	m.RequestID = fmt.Sprintf("consent-review-%d", c.next)
	c.h.Submit(ctx, hub.Inbound{From: c.agent, Msg: m})
	for {
		select {
		case response, ok := <-c.agent.Send:
			if !ok {
				return protocol.Message{}, fmt.Errorf("agent disconnected")
			}
			if response.RequestID != m.RequestID {
				continue
			}
			if response.Type == protocol.TypeError {
				return response, fmt.Errorf("%s: %s", response.Code, response.Detail)
			}
			return response, nil
		case <-ctx.Done():
			return protocol.Message{}, ctx.Err()
		}
	}
}

type consentPublicationFixture struct {
	h     *hub.Hub
	db    *store.Store
	ctx   context.Context
	owner *hub.OperatorSession
	c     *consentBridgeClient
	fake  *forge.Fake
	id    string
}
type consentBridgeVersionAccept interface {
	AcceptVersion(context.Context, string, messaging.Arrival) (messaging.Answer, error)
}
type consentBridgeLegacyAccept interface {
	Accept(context.Context, string, messaging.Message, time.Time) (messaging.Answer, error)
}

func consentBridgeAccept(t *testing.T, db *store.Store, key string, m messaging.Message) messaging.Answer {
	t.Helper()
	now := m.CreatedAt
	var a messaging.Answer
	var err error
	if s, ok := any(db.PeerMessages()).(consentBridgeVersionAccept); ok {
		a, err = s.AcceptVersion(context.Background(), key, messaging.Arrival{Version: 8, Message: m, At: now})
	} else {
		a, err = any(db.PeerMessages()).(consentBridgeLegacyAccept).Accept(context.Background(), key, m, now)
	}
	requireNoError(t, err)
	return a
}

func newConsentPublicationFixture(t *testing.T, kind string) *consentPublicationFixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "consent-publication.db"))
	requireNoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	h := hub.New(db, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f := &consentPublicationFixture{h: h, db: db, ctx: ctx, owner: hub.NewOperatorSession(1), fake: forge.NewFake()}
	now := time.Now().UTC()
	key := "sha256:" + fmt.Sprintf("%064x", 293)
	_, err = db.Commit(ctx, peer.PairingOutcome{OperationID: "consent-pair", InvitationID: "consent-inv", IdentityKey: key, Name: "Remote", TranscriptID: make([]byte, 64), At: now})
	requireNoError(t, err)
	_, err = db.Accept(ctx, key)
	requireNoError(t, err)
	_, err = db.Deliver(ctx, key, peer.PairingAccepted, now)
	requireNoError(t, err)
	_, err = db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "reviewer", SessionID: "offline-fixture"}, 24)
	requireNoError(t, err)
	requireNoError(t, db.SetConsentOptIn(ctx, store.ConsentOptIn{Contact: key, On: true, AuditID: 0, Now: now}))
	for _, op := range []string{protocol.ConsentReview, protocol.ConsentPublish} {
		requireNoError(t, db.ResetConsentScope(ctx, store.ConsentReset{Contact: key, Agent: "reviewer", Operation: op, AuditID: 0, Now: now}))
	}
	target, pins := reviewBridgeTarget(), reviewBridgePins()
	m := messaging.Message{ID: "publication-consent", N: 1, CreatedAt: now, From: "remote-sender", To: []string{"reviewer"}, Text: "review exact pins", Review: &messaging.ReviewAsk{Forge: target.Forge, Repository: target.Repository, Change: target.Change, Base: pins.Base, Head: pins.Head, Publish: true, Formal: true}}
	answer := consentBridgeAccept(t, db, key, m)
	if !answer.Held {
		t.Fatalf("review wasn't held: %+v", answer)
	}
	page, err := db.ConsentPage(ctx, key, 0, 10)
	requireNoError(t, err)
	if len(page.Items) != 1 {
		t.Fatalf("held views: %+v", page)
	}
	go h.Run(ctx)
	t.Cleanup(func() { cancel(); <-h.Done() })
	opened := h.OpenOperatorSession(ctx, protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "open"}, f.owner)
	if opened.Session == nil {
		t.Fatalf("owner open: %+v", opened)
	}
	v := page.Items[0]
	_, err = db.DecideConsent(ctx, store.ConsentDecision{ID: v.ID, Digest: v.Digest, Revision: v.Revision, Choice: kind, Evidence: store.ConsentEvidence{Kind: kind, Epoch: opened.Session.Epoch, Gen: opened.Session.Gen}, Now: now})
	requireNoError(t, err)
	// This fixture begins after dispatch; the hub remains the real authority
	// for the later pending/failed -> submitting publication reservation.
	imported, err := db.ImportRemote(ctx, store.RemoteImport{Contact: key, RemoteID: m.ID}, 1, now)
	requireNoError(t, err)
	if imported.ReviewID == "" {
		t.Fatalf("no dispatched review: %+v", imported)
	}
	f.id = imported.ReviewID
	f.fake.SetRevisions(target, pins)
	f.fake.SetChanges(target, []forge.FileChange{{Path: "worker.go", Lines: []forge.LineRange{{From: 4, To: 7}}}})
	agent := &hub.Client{ID: "reviewer", SessionID: "reviewer-live", ProtocolVersion: 24, Send: make(chan protocol.Message, 64)}
	f.c = &consentBridgeClient{h: h, agent: agent, forges: forge.Registry{"fake": f.fake}}
	h.Register(ctx, agent)
	_, err = acceptReview(ctx, f.c, f.id)
	requireNoError(t, err)
	return f
}

func (f *consentPublicationFixture) endOwner(t *testing.T) {
	t.Helper()
	f.h.CloseOperatorSession(f.owner)
	replacement := hub.NewOperatorSession(1)
	response := f.h.OpenOperatorSession(f.ctx, protocol.Message{Type: protocol.TypeOperatorSessionOpen, RequestID: "replacement"}, replacement)
	if response.Session == nil {
		t.Fatalf("replacement owner barrier: %+v", response)
	}
	f.owner = replacement
}
func (f *consentPublicationFixture) verdict() submitArgs {
	return submitArgs{ReviewID: f.id, Attempt: 1, Outcome: protocol.OutcomeApprove, Summary: "reviewed exact pins"}
}
func (f *consentPublicationFixture) view(t *testing.T) protocol.ReviewView {
	t.Helper()
	r, ok, err := f.db.GetReview(f.ctx, f.id)
	requireNoError(t, err)
	if !ok {
		t.Fatal("review vanished")
	}
	return r.View
}
