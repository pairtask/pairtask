package bridge

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestStaleAskBridgeAcceptanceCheckBeforeVerdict(t *testing.T) {
	for _, state := range []string{protocol.ReviewRequested, protocol.ReviewReady} {
		t.Run(state, func(t *testing.T) {
			c, _ := staleBridgeFixture(state)
			result, err := checkReview(context.Background(), c, c.view.ID)
			requireNoError(t, err)
			assertEqual(t, "matching check preserves state", result.Review.State, state)
			last := staleBridgeLast(t, c)
			assertEqual(t, "atomic pin comparison", last.Type, "review_check_pins")
			assertEqual(t, "resolved pins", *last.Revisions, reviewBridgePins())
			assertEqual(t, "no task on check", result.Task == nil, true)
		})
	}
}

func TestStaleAskBridgeAcceptanceMovedAcceptReturnsNoTask(t *testing.T) {
	for _, state := range []string{protocol.ReviewRequested, protocol.ReviewReady} {
		t.Run(state, func(t *testing.T) {
			c, fake := staleBridgeFixture(state)
			moved := reviewBridgePins()
			moved.Head = moved.Base
			fake.SetRevisions(reviewBridgeTarget(), moved)
			result, err := acceptReview(context.Background(), c, c.view.ID)
			requireNoError(t, err)
			assertEqual(t, "moved request declined", result.Review.State, protocol.ReviewDeclined)
			assertEqual(t, "no misleading accepted task", result.Task == nil, true)
			assertEqual(t, "explicit acceptance result", staleBridgeFields(t, result)["accepted"], any(false))
			assertEqual(t, "last call compares rather than accepts", staleBridgeLast(t, c).Type, "review_check_pins")
		})
	}
}

func TestStaleAskBridgeAcceptanceMatchingAcceptReturnsTask(t *testing.T) {
	c, _ := staleBridgeFixture(protocol.ReviewReady)
	result, err := acceptReview(context.Background(), c, c.view.ID)
	requireNoError(t, err)
	assertEqual(t, "accepted", staleBridgeFields(t, result)["accepted"], any(true))
	if result.Task == nil {
		t.Fatal("matching request has no task")
	}
	assertEqual(t, "original pinned task", result.Task.Head, reviewBridgePins().Head)
	assertEqual(t, "state", result.Review.State, protocol.ReviewInReview)
}

func TestStaleAskBridgeAcceptanceForgeFailureDoesNotWrite(t *testing.T) {
	for _, accept := range []bool{false, true} {
		c, _ := staleBridgeFixture(protocol.ReviewReady)
		c.forges = forge.Registry{"fake": &advisoryBridgeForge{Forge: forge.NewFake(), started: make(chan struct{}), err: context.DeadlineExceeded}}
		var err error
		if accept {
			_, err = acceptReview(context.Background(), c, c.view.ID)
		} else {
			_, err = checkReview(context.Background(), c, c.view.ID)
		}
		if err == nil {
			t.Fatal("forge failure was hidden")
		}
		assertEqual(t, "only ownership read sent", len(c.calls), 1)
		assertEqual(t, "read before forge failure", c.calls[0].Type, protocol.TypeReviewGet)
		assertEqual(t, "state untouched", c.view.State, protocol.ReviewReady)
	}
}

func TestStaleAskBridgeAcceptanceLegacyReadyUsesExistingAccept(t *testing.T) {
	c, _ := staleBridgeFixture(protocol.ReviewReady)
	c.version = 25
	result, err := acceptReview(context.Background(), c, c.view.ID)
	requireNoError(t, err)
	assertEqual(t, "legacy accepted", result.Review.State, protocol.ReviewInReview)
	assertEqual(t, "legacy has task", result.Task != nil, true)
	for _, call := range c.calls {
		if call.Type == "review_check_pins" {
			t.Fatal("legacy broker received unsupported comparison")
		}
	}
}
