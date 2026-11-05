package messaging_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"testing"
	"time"
)

type consentReviewTerminalPair struct {
	left, right         *store.Store
	leftPath, rightPath string
	id                  string
	view                protocol.ConsentView
}

func consentReviewExchangeAt(t *testing.T, left, right messaging.Store, now time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ab, ba := make(chan []byte, 1), make(chan []byte, 1)
	done := make(chan sessionOutcome, 2)
	go func() {
		s := sessionSide(channelFrames{ctx, ba, ab, 0}, left)
		s.Now = func() time.Time { return now }
		r, err := s.Dial(ctx)
		if err != nil {
			cancel()
		}
		done <- sessionOutcome{r, err}
	}()
	go func() {
		s := sessionSide(channelFrames{ctx, ab, ba, 0}, right)
		s.Now = func() time.Time { return now }
		r, err := s.Serve(ctx)
		if err != nil {
			cancel()
		}
		done <- sessionOutcome{r, err}
	}()
	sessionSuccess(t, []sessionOutcome{<-done, <-done})
}
func newConsentReviewTerminalPair(t *testing.T) consentReviewTerminalPair {
	t.Helper()
	left, lp := sessionFixture(t)
	right, rp := sessionFixture(t)
	ctx := context.Background()
	_, err := right.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "reviewer", SessionID: "offline"}, 24)
	sessionOK(t, err)
	sessionOK(t, right.SetConsentOptIn(ctx, store.ConsentOptIn{Contact: sessionContact, On: true, AuditID: 0, Now: sessionAt}))
	for _, op := range []string{"review", "publish"} {
		sessionOK(t, right.ResetConsentScope(ctx, store.ConsentReset{Contact: sessionContact, Agent: "reviewer", Operation: op, AuditID: 0, Now: sessionAt}))
	}
	p := consentReviewTerminalPair{left: left, right: right, leftPath: lp, rightPath: rp, id: "correlated-terminal-review"}
	consentQueueReview(t, left, consentReviewOutgoing(sessionContact, p.id, sessionAt))
	consentReviewExchangeAt(t, left.PeerMessages(), right.PeerMessages(), sessionAt)
	page, err := right.ConsentPage(ctx, sessionContact, 0, 20)
	sessionOK(t, err)
	sessionEqual(t, len(page.Items), 1)
	p.view = page.Items[0]
	return p
}
func consentQueueReview(t *testing.T, db *store.Store, out messaging.Outgoing) {
	t.Helper()
	_, err := db.PeerMessages().Queue(context.Background(), out)
	sessionOK(t, err)
}
func consentReviewTerminalUnrelated(t *testing.T, p consentReviewTerminalPair) map[string]protocol.ReviewAskedItem {
	t.Helper()
	now := sessionAt.Add(6 * 24 * time.Hour)
	consentQueueReview(t, p.left, consentReviewOutgoing(sessionContact, "unrelated-same-contact", now))
	sessionOK(t, p.left.PeerMessages().Delivered(context.Background(), sessionContact, "unrelated-same-contact", now))
	other := consentRefusalOtherContact(t, p.left)
	consentQueueReview(t, p.left, consentReviewOutgoing(other, p.id, now))
	page, err := p.left.AskedReviews(context.Background(), "")
	sessionOK(t, err)
	items := map[string]protocol.ReviewAskedItem{}
	for _, item := range page.Items {
		if item.Contact != sessionContact || item.AskID != p.id {
			items[item.Contact+"/"+item.AskID] = item
		}
	}
	sessionEqual(t, len(items), 2)
	return items
}

func consentReviewOutgoing(contact, id string, now time.Time) messaging.Outgoing {
	return messaging.Outgoing{Contact: contact, ID: id, Text: "one immutable review", To: []string{"reviewer"}, Review: &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "293", Base: "base", Head: "head", Publish: true}, Now: now}
}
