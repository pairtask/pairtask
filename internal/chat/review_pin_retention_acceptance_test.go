package chat

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Resource assertions live in-package so the test can count retained
// cancellation registrations deterministically, without heap/GC timing.
func pinRetentionCard(t *testing.T, m *Model, id string) (remoteAsk, protocol.Revisions, *actionCard) {
	t.Helper()
	pins := protocol.Revisions{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
	c := &actionCard{id: id, kind: cardRemoteReview, review: &reviewProgress{pins: &pins, item: &protocol.ReviewAskedItem{State: "completed", Settled: true}}}
	if !m.startCard(c) {
		t.Fatal("completed-card fixture exhausted active-card capacity")
	}
	return remoteAsk{card: id, target: protocol.ReviewTarget{Change: id}}, pins, c
}

func pinRetentionFinish(t *testing.T, m *Model, ask remoteAsk, pins protocol.Revisions) {
	t.Helper()
	cmd := m.checkPinsAfterSending(ask, pins)
	if cmd == nil {
		t.Fatal("pin check command was not created")
	}
	m.pinsChecked(cmd().(pinCheckedMsg))
}

func TestReviewPinRetentionAcceptanceCompletedChecksReleaseRegistrations(t *testing.T) {
	m := New(Options{Bounds: Bounds{CheckPins: func(context.Context, protocol.ReviewTarget, protocol.Revisions) []protocol.ReviewAdvisory { return nil }}})
	for i := 0; i < cardWindow*3; i++ {
		ask, pins, _ := pinRetentionCard(t, m, fmt.Sprintf("completed-%d", i))
		pinRetentionFinish(t, m, ask, pins)
		if len(m.cards.pinCancels) != 0 {
			t.Fatalf("completed check %d still retains %d cancel registrations", i, len(m.cards.pinCancels))
		}
	}
	if len(m.cards.list) > cardWindow {
		t.Fatal("retention fixture bypassed the real card window")
	}
}

func TestReviewPinRetentionAcceptanceEvictedCardCompletionStillCleansUp(t *testing.T) {
	m := New(Options{Bounds: Bounds{CheckPins: func(context.Context, protocol.ReviewTarget, protocol.Revisions) []protocol.ReviewAdvisory { return nil }}})
	ask, pins, evicted := pinRetentionCard(t, m, "evicted")
	cmd := m.checkPinsAfterSending(ask, pins)
	for i := 0; i < cardWindow; i++ {
		pinRetentionCard(t, m, fmt.Sprintf("replacement-%d", i))
	}
	if m.card(evicted.id) != nil {
		t.Fatal("fixture did not evict the completed card")
	}
	m.pinsChecked(cmd().(pinCheckedMsg))
	if len(m.cards.pinCancels) != 0 {
		t.Fatal("early return for an evicted card leaked its completed check")
	}
	if len(evicted.review.pinSteps) != 0 {
		t.Fatal("evicted card accepted a late presentation update")
	}
}

func TestReviewPinRetentionAcceptanceOldGenerationCleanupCannotRemoveFreshCheck(t *testing.T) {
	seen := map[string]error{}
	m := New(Options{Bounds: Bounds{CheckPins: func(ctx context.Context, target protocol.ReviewTarget, _ protocol.Revisions) []protocol.ReviewAdvisory {
		seen[target.Change] = ctx.Err()
		return nil
	}}})
	old, pins, oldCard := pinRetentionCard(t, m, "old")
	oldCmd := m.checkPinsAfterSending(old, pins)
	m.cancelPinChecks()
	fresh, pins, freshCard := pinRetentionCard(t, m, "fresh")
	freshCmd := m.checkPinsAfterSending(fresh, pins)
	m.pinsChecked(oldCmd().(pinCheckedMsg))
	if len(m.cards.pinCancels) != 1 {
		t.Fatal("old completion removed the fresh generation's cancellation registration")
	}
	if seen["old"] == nil {
		t.Fatal("old generation context was not cancelled")
	}
	if len(oldCard.review.pinSteps) != 0 {
		t.Fatal("old generation updated its card")
	}
	m.pinsChecked(freshCmd().(pinCheckedMsg))
	if seen["fresh"] != nil {
		t.Fatal("old completion cancelled the fresh context")
	}
	if len(m.cards.pinCancels) != 0 {
		t.Fatal("fresh completion retained its cancellation registration")
	}
	if len(freshCard.review.pinSteps) == 0 {
		t.Fatal("fresh generation lost its presentation result")
	}
}
