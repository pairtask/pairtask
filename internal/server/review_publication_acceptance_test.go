//go:build linux

package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func reviewTestComplete(f *operatorFixture, p *sessionProbe, id string) {
	f.t.Helper()
	reviewTestStart(f, p, id)
	reviewTestView(f.t, reviewTestCall(p, "review_submit", id, map[string]any{"attempt": 1, "verdict": reviewTestVerdict()}), "review_result")
}

func (p *sessionProbe) reviewPublication(id, from, to string, extra map[string]any) sessionWire {
	p.t.Helper()
	if extra == nil {
		extra = map[string]any{}
	}
	extra["from"], extra["to"] = from, to
	if to == "submitting" {
		extra["identity"] = "reviewer-account"
	}
	return reviewTestCall(p, "review_publication", id, map[string]any{"attempt": 1, "publication": extra})
}

func reviewTestReceipt() protocol.PublicationReceipt {
	return protocol.PublicationReceipt{ReviewID: "provider-review-42", URL: "https://github.com/team/project/pull/201#pullrequestreview-42", Identity: "reviewer-account", Revision: reviewTestRevisions().Head, Formal: true}
}

func TestReviewAcceptancePublicationIntentRejectsReplayAndLateResponses(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	reviewTestComplete(f, p, "publication")
	first := p.reviewPublication("publication", "pending", "submitting", map[string]any{"token": "write-one"})
	operatorEqual(t, "first writer authorized", sessionDecode[bool](t, first, "intent_acquired"), true)
	one := reviewTestView(t, first, "review_result").Publication.Intent
	operatorEqual(t, "first generation nonzero", one > 0, true)
	repeated := p.reviewPublication("publication", "pending", "submitting", map[string]any{"token": "write-one"})
	operatorEqual(t, "replay never authorizes another POST", reviewTestIntentAcquired(t, repeated), false)
	operatorEqual(t, "same intent on replay", reviewTestView(t, repeated, "review_result").Publication.Intent, one)
	operatorEqual(t, "second writer cannot acquire", p.reviewPublication("publication", "pending", "submitting", map[string]any{"token": "write-other"}).text("code"), "review_conflict")
	reviewTestView(t, p.reviewPublication("publication", "submitting", "failed", map[string]any{"intent": one, "reason": "forbidden"}), "review_result")
	retry := p.reviewPublication("publication", "failed", "submitting", map[string]any{"token": "write-two"})
	two := reviewTestView(t, retry, "review_result").Publication.Intent
	operatorEqual(t, "retry is new generation", two > one, true)
	operatorEqual(t, "retry acquires intent", sessionDecode[bool](t, retry, "intent_acquired"), true)
	operatorEqual(t, "late response cannot win ABA", p.reviewPublication("publication", "submitting", "published", map[string]any{"intent": one, "receipt": reviewTestReceipt()}).text("code"), "review_conflict")
	done := reviewTestView(t, p.reviewPublication("publication", "submitting", "published", map[string]any{"intent": two, "receipt": reviewTestReceipt()}), "review_result")
	operatorEqual(t, "provider success recorded", done.Publication.State, "published")
	operatorEqual(t, "exact receipt", *done.Publication.Receipt, reviewTestReceipt())
}

func TestReviewAcceptanceUncertainPublicationCannotBlindRetry(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	reviewTestComplete(f, p, "uncertain")
	intent := reviewTestView(t, p.reviewPublication("uncertain", "pending", "submitting", map[string]any{"token": "first-write"}), "review_result").Publication.Intent
	uncertain := reviewTestView(t, p.reviewPublication("uncertain", "submitting", "uncertain", map[string]any{"intent": intent, "reason": "unavailable"}), "review_result")
	operatorEqual(t, "analysis stays complete", uncertain.State, "completed")
	operatorEqual(t, "result survives transport error", uncertain.Verdict != nil, true)
	for _, next := range []string{"pending", "failed", "submitting"} {
		operatorEqual(t, "uncertainty cannot become "+next, p.reviewPublication("uncertain", "uncertain", next, map[string]any{"intent": intent, "token": "blind-retry", "reason": "not_found"}).text("type"), "error")
	}
	found := reviewTestView(t, p.reviewPublication("uncertain", "uncertain", "published", map[string]any{"intent": intent, "receipt": reviewTestReceipt()}), "review_result")
	operatorEqual(t, "reconciliation retains actual provider receipt", *found.Publication.Receipt, reviewTestReceipt())
}

func TestReviewAcceptanceFailedPublicationCanBecomeStale(t *testing.T) {
	for _, field := range []string{"base", "head"} {
		t.Run(field, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			p := reviewTestPeer(f, "alpha")
			reviewTestComplete(f, p, "moved")
			intent := reviewTestView(t, p.reviewPublication("moved", "pending", "submitting", map[string]any{"token": "first"}), "review_result").Publication.Intent
			reviewTestView(t, p.reviewPublication("moved", "submitting", "failed", map[string]any{"intent": intent, "reason": "rate_limited"}), "review_result")
			observed := reviewTestRevisions()
			if field == "base" {
				observed.Base = strings.Repeat("c", 40)
			} else {
				observed.Head = strings.Repeat("c", 40)
			}
			stale := reviewTestView(t, p.reviewPublication("moved", "failed", "stale", map[string]any{"observed": observed, "reason": "moved"}), "review_result")
			operatorEqual(t, "drift retains original pins", *stale.Revisions, reviewTestRevisions())
			operatorEqual(t, "observed drift exposed", *stale.Stale, observed)
			operatorEqual(t, "stale verdict not republished", p.reviewPublication("moved", "stale", "submitting", map[string]any{"token": "retry"}).text("code"), "review_conflict")
		})
	}
}

func TestReviewAcceptancePublicationIntentSurvivesBrokerRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "publication.db")
	var intent int
	t.Run("write-intent", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := reviewTestPeer(f, "alpha")
		reviewTestComplete(f, p, "crashed-write")
		intent = reviewTestView(t, p.reviewPublication("crashed-write", "pending", "submitting", map[string]any{"token": "persisted-intent"}), "review_result").Publication.Intent
	})
	if t.Failed() {
		return
	}
	t.Run("recover-before-provider-response", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := reviewTestPeer(f, "alpha")
		got := reviewTestView(t, reviewTestCall(p, "review_get", "crashed-write", nil), "review_result")
		operatorEqual(t, "write intent survives", got.Publication.Intent, intent)
		operatorEqual(t, "crash does not reset to pending", got.Publication.State, "submitting")
		repeated := p.reviewPublication("crashed-write", "pending", "submitting", map[string]any{"token": "persisted-intent"})
		reviewTestView(t, repeated, "review_result")
		operatorEqual(t, "restart replay grants no write", reviewTestIntentAcquired(t, repeated), false)
		reviewTestView(t, p.reviewPublication("crashed-write", "submitting", "uncertain", map[string]any{"intent": intent, "reason": "unavailable"}), "review_result")
		reviewTestView(t, p.reviewPublication("crashed-write", "uncertain", "published", map[string]any{"intent": intent, "receipt": reviewTestReceipt()}), "review_result")
	})
	if t.Failed() {
		return
	}
	t.Run("recover-receipt", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		got := reviewTestView(t, reviewTestCall(reviewTestPeer(f, "alpha"), "review_get", "crashed-write", nil), "review_result")
		operatorEqual(t, "receipt persists", *got.Publication.Receipt, reviewTestReceipt())
		operatorEqual(t, "published durable", got.Publication.State, "published")
	})
}

// False capability flags may be omitted on the wire; only explicit true
// authorizes a provider write.
func reviewTestIntentAcquired(t *testing.T, r sessionWire) bool {
	t.Helper()
	if len(r["intent_acquired"]) == 0 {
		return false
	}
	var acquired bool
	operatorNoError(t, json.Unmarshal(r["intent_acquired"], &acquired))
	return acquired
}

func TestReviewAcceptanceRefreshRetainsPublishedReceiptAndPins(t *testing.T) {
	for _, field := range []string{"base", "head"} {
		t.Run(field, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			p := reviewTestPeer(f, "alpha")
			reviewTestComplete(f, p, "refresh")
			intent := reviewTestView(t, p.reviewPublication("refresh", "pending", "submitting", map[string]any{"token": "publish"}), "review_result").Publication.Intent
			reviewTestView(t, p.reviewPublication("refresh", "submitting", "published", map[string]any{"intent": intent, "receipt": reviewTestReceipt()}), "review_result")
			moved := reviewTestRevisions()
			if field == "base" {
				moved.Base = strings.Repeat("c", 40)
			} else {
				moved.Head = strings.Repeat("c", 40)
			}
			got := reviewTestView(t, reviewTestCall(p, "review_observe", "refresh", map[string]any{"revisions": moved, "attempt": 1}), "review_result")
			operatorEqual(t, "freshness check records either drift", *got.Stale, moved)
			operatorEqual(t, "original review revisions retained", *got.Revisions, reviewTestRevisions())
			operatorEqual(t, "published receipt still visible", *got.Publication.Receipt, reviewTestReceipt())
			operatorEqual(t, "publication truth retained", got.Publication.State, "published")
		})
	}
}

func TestReviewAcceptanceProviderRevisionMismatchRemainsVisible(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	reviewTestComplete(f, p, "provider-mismatch")
	intent := reviewTestView(t, p.reviewPublication("provider-mismatch", "pending", "submitting", map[string]any{"token": "publish"}), "review_result").Publication.Intent
	receipt := reviewTestReceipt()
	receipt.Revision = strings.Repeat("d", 40)
	got := reviewTestView(t, p.reviewPublication("provider-mismatch", "submitting", "published", map[string]any{"intent": intent, "receipt": receipt}), "review_result")
	operatorEqual(t, "actual provider outcome retained", *got.Publication.Receipt, receipt)
	operatorEqual(t, "mismatch is explicit", got.Stale != nil, true)
	operatorEqual(t, "recorded submitted revision", got.Stale.Head, receipt.Revision)
	operatorEqual(t, "pins are never overwritten", *got.Revisions, reviewTestRevisions())
}

func TestReviewAcceptanceObservationFencesOldAttemptAndBlocksStaleWrite(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	reviewTestStart(f, p, "observe-before-write")
	reviewTestView(t, reviewTestCall(p, "review_restart", "observe-before-write", map[string]any{"attempt": 1}), "review_result")
	reviewTestView(t, reviewTestCall(p, "review_submit", "observe-before-write", map[string]any{"attempt": 2, "verdict": reviewTestVerdict()}), "review_result")
	moved := reviewTestRevisions()
	moved.Base = strings.Repeat("c", 40)
	operatorEqual(t, "superseded observer refused", reviewTestCall(p, "review_observe", "observe-before-write", map[string]any{"attempt": 1, "revisions": moved}).text("code"), "review_conflict")
	reviewTestView(t, reviewTestCall(p, "review_observe", "observe-before-write", map[string]any{"attempt": 2, "revisions": moved}), "review_result")
	r := reviewTestCall(p, "review_publication", "observe-before-write", map[string]any{"attempt": 2, "publication": map[string]any{"from": "pending", "to": "submitting", "identity": "reviewer-account", "token": "stale-write"}})
	operatorEqual(t, "observed drift blocks new provider write", r.text("code"), "review_conflict")
	operatorEqual(t, "no stale write authorized", reviewTestIntentAcquired(t, r), false)
}
