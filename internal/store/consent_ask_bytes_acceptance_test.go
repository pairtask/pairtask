package store_test

import (
	"context"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"testing"
	"time"
)

func consentAssertAskByteBound(t *testing.T, global bool, over int) {
	t.Helper()
	f := consentAskByteFixture(t, global)
	m := consentSizedAsk(t, over)
	before := consentSnapshot(t, f)
	a, err := f.consentAccept(t, 7, m, messageAt.Add(20*time.Minute))
	pairOK(t, err)
	if over == 0 {
		pairEqual(t, a.Stored, true)
		pairEqual(t, a.Held, false)
		return
	}
	pairEqual(t, a.Stored, false)
	pairEqual(t, a.Held, false)
	pairEqual(t, a.Reason, messaging.ReasonInboxFull)
	consentUnchanged(t, f, before)
}
func TestConsentAskBytesAcceptanceLegacyAskSharesContactByteBound(t *testing.T) {
	t.Run("exact", func(t *testing.T) { consentAssertAskByteBound(t, false, 0) })
	t.Run("one-over", func(t *testing.T) { consentAssertAskByteBound(t, false, 1) })
}
func TestConsentAskBytesAcceptanceLegacyAskSharesGlobalByteBound(t *testing.T) {
	t.Run("exact", func(t *testing.T) { consentAssertAskByteBound(t, true, 0) })
	t.Run("one-over", func(t *testing.T) { consentAssertAskByteBound(t, true, 1) })
}
func TestConsentAskBytesAcceptanceFailedAdmissionDoesNotConsumeByteRoom(t *testing.T) {
	f := consentAskByteFixture(t, false)
	m := consentSizedAsk(t, 0)
	ctx := context.Background()
	_, err := f.raw.Exec(`CREATE TRIGGER fail_ask_bytes BEFORE INSERT ON remote_imports BEGIN SELECT RAISE(ABORT,'injected legacy ask failure'); END`)
	pairOK(t, err)
	before := consentSnapshot(t, f)
	a, err := f.consentAccept(t, 7, m, messageAt.Add(20*time.Minute))
	if err == nil || a.Stored {
		t.Fatal("injected legacy ask insertion did not roll back")
	}
	consentUnchanged(t, f, before)
	_, err = f.raw.Exec(`DROP TRIGGER fail_ask_bytes`)
	pairOK(t, err)
	a, err = f.consentAccept(t, 7, m, messageAt.Add(20*time.Minute))
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	next := m
	next.ID = "no-byte-room-after-exact"
	next.N++
	next.Text = "x"
	a, err = f.consentAccept(t, 7, next, messageAt.Add(21*time.Minute))
	pairOK(t, err)
	pairEqual(t, a.Stored, false)
	pairEqual(t, a.Reason, messaging.ReasonInboxFull)
	pending, err := f.db.PendingImports(ctx, 20)
	pairOK(t, err)
	pairEqual(t, len(pending), 0)
}
