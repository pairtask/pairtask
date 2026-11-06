package store_test

import (
	"context"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
	"sync"
	"testing"
	"time"
)

func TestConsentReplayAcceptancePreviewIsReadOnlyAndIsolated(t *testing.T) {
	f := consentFixture(t, "alpha", "beta")
	m := consentMessage("private", 1, "alpha", "beta")
	v := consentHeld(t, f, m, messageAt)
	identityMissing := v.Contact != f.key || v.Digest == "" || v.Revision <= 0
	payloadMissing := v.Text != m.Text || len(v.Targets) != 2
	if identityMissing || payloadMissing {
		t.Fatalf("incomplete immutable preview: %+v", v)
	}
	pairEqual(t, v.ExpiresAt.Equal(messageAt.Add(7*24*time.Hour)), true)
	pending, err := f.db.PendingImports(context.Background(), 20)
	pairOK(t, err)
	pairEqual(t, len(pending), 0)
	before := consentSnapshot(t, f)
	for i := 0; i < 3; i++ {
		_, err := f.db.ConsentPage(context.Background(), f.key, 0, 20)
		pairOK(t, err)
	}
	consentUnchanged(t, f, before)
}

func TestConsentReplayAcceptanceDuplicatesDoNotChargeOrExtend(t *testing.T) {
	f := consentFixture(t, "alpha")
	m := consentMessage("duplicate", 1, "alpha")
	v := consentHeld(t, f, m, messageAt)
	before := consentSnapshot(t, f)
	a, err := f.consentAccept(t, 8, m, messageAt.Add(time.Hour))
	pairOK(t, err)
	pairEqual(t, a.HeldID, v.ID)
	consentUnchanged(t, f, before)
}

func TestConsentReplayAcceptanceChangedPayloadCannotReplaceSnapshot(t *testing.T) {
	for _, change := range []string{"text", "sender", "recipient"} {
		t.Run(change, func(t *testing.T) {
			f := consentFixture(t, "alpha", "beta")
			m := consentMessage("conflict", 1, "alpha")
			consentHeld(t, f, m, messageAt)
			before := consentSnapshot(t, f)
			switch change {
			case "text":
				m.Text = "replacement"
			case "sender":
				m.From = "other"
			case "recipient":
				m.To = []string{"beta"}
			}
			a, err := f.consentAccept(t, 8, m, messageAt.Add(time.Minute))
			conflict := errors.Is(err, store.ErrConsentConflict) || errors.Is(err, messaging.ErrConflict)
			if !conflict && a.Reason != messaging.ReasonConflict {
				t.Fatalf("changed immutable payload accepted: answer=%+v err=%v", a, err)
			}
			consentUnchanged(t, f, before)
		})
	}
}

func TestConsentReplayAcceptanceConcurrentSameRequestHasOneHold(t *testing.T) {
	f := consentFixture(t, "alpha")
	m := consentMessage("concurrent", 1, "alpha")
	start := make(chan struct{})
	var wg sync.WaitGroup
	answers := make(chan messaging.Answer, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			a, e := f.consentAccept(t, 8, m, messageAt)
			answers <- a
			errs <- e
		}()
	}
	close(start)
	wg.Wait()
	close(answers)
	close(errs)
	for err := range errs {
		pairOK(t, err)
	}
	id := ""
	for a := range answers {
		if !a.Held {
			t.Fatalf("concurrent request not held: %+v", a)
		}
		if id == "" {
			id = a.HeldID
		}
		pairEqual(t, a.HeldID, id)
	}
	page, err := f.db.ConsentPage(context.Background(), f.key, 0, 20)
	pairOK(t, err)
	pairEqual(t, page.Total, 1)
}

func TestConsentReplayAcceptanceV7CannotReceiveHeldOutcome(t *testing.T) {
	f := consentFixture(t, "alpha")
	a, err := f.consentAccept(t, 7, consentMessage("old-peer", 1, "alpha"), messageAt)
	pairOK(t, err)
	pairEqual(t, a.Held, false)
	pairEqual(t, a.Reason, messaging.ReasonNotShared)
	page, err := f.db.ConsentPage(context.Background(), f.key, 0, 20)
	pairOK(t, err)
	pairEqual(t, page.Total, 0)
}

func TestConsentReplayAcceptanceStaleDecisionCannotDispatch(t *testing.T) {
	for _, field := range []string{"digest", "revision"} {
		t.Run(field, func(t *testing.T) {
			f := consentFixture(t, "alpha")
			v := consentHeld(t, f, consentMessage("stale", 1, "alpha"), messageAt)
			d := consentDecision(v, protocol.ConsentOnce, messageAt)
			if field == "digest" {
				d.Digest = "changed"
			} else {
				d.Revision++
			}
			before := consentSnapshot(t, f)
			_, err := f.db.DecideConsent(context.Background(), d)
			if !errors.Is(err, store.ErrConsentStale) {
				t.Fatalf("stale decision returned %v", err)
			}
			consentUnchanged(t, f, before)
		})
	}
}
