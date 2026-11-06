package messaging_test

import (
	"context"
	"errors"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"io"
	"testing"
)

type failingConsentSettlement struct{ *consentOutcomeProbe }

func (p *failingConsentSettlement) ConsentOutcomeAnswered(context.Context, string, messaging.ConsentOutcomeStored) error {
	return io.ErrClosedPipe
}
func (p *failingConsentSettlement) ConsentOutcomeRefused(context.Context, string, messaging.ConsentOutcomeRefused) error {
	return io.ErrClosedPipe
}
func consentAssertSettlementFailed(t *testing.T, kind byte) {
	t.Helper()
	p := &failingConsentSettlement{newConsentOutcomeProbe(t)}
	fields := []string{"request"}
	if kind == 26 {
		fields = append(fields, "terminal_conflict")
	}
	frames := &scriptFrames{input: append(consentOutcomePrefix(t, 8), consentRawFrame(kind, fields...))}
	result, _, err := sessionSide(frames, p).OpenDial(context.Background())
	sessionEqual(t, errors.Is(err, io.ErrClosedPipe), true)
	var local messaging.StoreError
	sessionEqual(t, errors.As(err, &local), true)
	sessionEqual(t, result.ConsentOutcomesSent, 0)
	sessionEqual(t, result.ConsentOutcomesRefused, 0)
	sessionEqual(t, len(p.pendingConsent), 1)
}
func TestConsentSettlementFailureAcceptanceFailedStoredAckIsNotCounted(t *testing.T) {
	consentAssertSettlementFailed(t, 25)
}
func TestConsentSettlementFailureAcceptanceFailedRefusalIsNotCounted(t *testing.T) {
	consentAssertSettlementFailed(t, 26)
}
