package messaging_test

import (
	"context"
	"encoding/json"
	"github.com/pairtask/pairtask/internal/store"
	"reflect"
	"testing"
	"time"
)

func consentAssertTerminalReview(t *testing.T, p consentReviewTerminalPair, state string, others map[string]any) {
	t.Helper()
	page, err := p.left.AskedReviews(context.Background(), "")
	sessionOK(t, err)
	sessionEqual(t, page.Total, 3)
	sessionEqual(t, len(page.Items), 3)
	matched := 0
	for _, item := range page.Items {
		key := item.Contact + "/" + item.AskID
		if item.Contact != sessionContact || item.AskID != p.id {
			sessionEqual(t, reflect.DeepEqual(item, others[key]), true)
			continue
		}
		matched++
		sessionEqual(t, item.Settled, true)
		if item.State == "asked" || item.State == "waiting" {
			t.Fatalf("terminal consent stranded active review ask: %+v", item)
		}
		sessionEqual(t, item.Verdict, "")
		sessionEqual(t, item.URL, "")
		sessionEqual(t, item.ReviewID, "")
		data, err := json.Marshal(item)
		sessionOK(t, err)
		var row struct {
			Consent string `json:"consent"`
		}
		sessionOK(t, json.Unmarshal(data, &row))
		sessionEqual(t, row.Consent, state)
	}
	sessionEqual(t, matched, 1)
}

func TestConsentReviewTerminalAcceptanceDeclineExpirySettleOneCorrelatedAskAcrossReconnect(t *testing.T) {
	for _, state := range []string{"declined", "expired"} {
		t.Run(state, func(t *testing.T) {
			p := newConsentReviewTerminalPair(t)
			ctx := context.Background()
			others := map[string]any{}
			for key, item := range consentReviewTerminalUnrelated(t, p) {
				others[key] = item
			}
			now := sessionAt.Add(7 * 24 * time.Hour)
			if state == "declined" {
				now = sessionAt.Add(6*24*time.Hour + time.Hour)
			}
			if state == "declined" {
				_, err := p.right.DecideConsent(ctx, store.ConsentDecision{ID: p.view.ID, Digest: p.view.Digest, Revision: p.view.Revision, Choice: "decline", Now: now})
				sessionOK(t, err)
			} else {
				count, err := p.right.ExpireConsent(ctx, now)
				sessionOK(t, err)
				sessionEqual(t, count, 1)
			}
			consentReviewExchangeAt(t, p.left.PeerMessages(), p.right.PeerMessages(), now)
			consentAssertTerminalReview(t, p, state, others)
			sessionOK(t, p.left.Close())
			sessionOK(t, p.right.Close())
			p.left = sessionDB(t, p.leftPath)
			p.right = sessionDB(t, p.rightPath)
			consentReviewExchangeAt(t, p.left.PeerMessages(), p.right.PeerMessages(), now.Add(time.Hour))
			consentAssertTerminalReview(t, p, state, others)
		})
	}
}

func TestConsentReviewTerminalAcceptanceAllowedDoesNotFabricateCompletion(t *testing.T) {
	p := newConsentReviewTerminalPair(t)
	ctx := context.Background()
	_, err := p.right.DecideConsent(ctx, store.ConsentDecision{ID: p.view.ID, Digest: p.view.Digest, Revision: p.view.Revision, Choice: "once", Evidence: store.ConsentEvidence{Kind: "once"}, Now: sessionAt})
	sessionOK(t, err)
	pending, err := p.right.PendingImports(ctx, 20)
	sessionOK(t, err)
	sessionEqual(t, len(pending), 1)
	_, err = p.right.ImportRemote(ctx, pending[0], 1, sessionAt)
	sessionOK(t, err)
	consentReviewExchangeAt(t, p.left.PeerMessages(), p.right.PeerMessages(), sessionAt)
	page, err := p.left.AskedReviews(ctx, "")
	sessionOK(t, err)
	sessionEqual(t, len(page.Items), 1)
	item := page.Items[0]
	sessionEqual(t, item.Settled, false)
	sessionEqual(t, item.Verdict, "")
	sessionEqual(t, item.URL, "")
	if item.State == "completed" || item.State == "published" {
		t.Fatalf("allowed admission fabricated review completion: %+v", item)
	}
	data, err := json.Marshal(item)
	sessionOK(t, err)
	var row struct {
		Consent string `json:"consent"`
	}
	sessionOK(t, json.Unmarshal(data, &row))
	sessionEqual(t, row.Consent, "allowed")
}
