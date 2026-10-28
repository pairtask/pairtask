//go:build linux

package bridge

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReviewBridgeAcceptanceRepeatedSubmitRecoversSavedResultWithoutDiffFetch(t *testing.T) {
	f := newReviewBridgeFixture(t)
	f.request(nil)
	f.accept()
	f.tool("submit_review", reviewBridgeVerdict("approve"))
	before := f.view()
	f.forge.Fail(forge.OpChanges, forge.Errorf(forge.ClassUnavailable, "saved review must not be analyzed again"))
	r := f.tool("submit_review", reviewBridgeVerdict("approve"))
	assertEqual(t, "identical submit retry recovers result", r.IsError, false)
	assertEqual(t, "retry retains exact saved state", reflect.DeepEqual(f.view(), before), true)
	assertEqual(t, "retry never duplicates published review", len(f.forge.Attempts()), 1)
}

func TestReviewBridgeAcceptanceUnclassifiedLostResponseIsUncertain(t *testing.T) {
	f := newReviewBridgeFixture(t)
	f.request(nil)
	f.accept()
	f.forge.LandThenFail(errors.New("connection closed before response"))
	f.tool("submit_review", reviewBridgeVerdict("approve"))
	assertEqual(t, "unknown post-write error cannot authorize retry", f.view().Publication.State, "uncertain")
	f.tool("publish_review", nil)
	assertEqual(t, "existing write reconciled", f.view().Publication.State, "published")
	assertEqual(t, "no duplicate after unclassified error", len(f.forge.Attempts()), 1)
}

func TestReviewBridgeAcceptanceUncertainMissNeverAuthorizesAnotherWrite(t *testing.T) {
	f := newReviewBridgeFixture(t)
	f.request(nil)
	f.accept()
	f.forge.Fail(forge.OpPublish, forge.Errorf(forge.ClassUncertain, "write outcome unknown"))
	f.tool("submit_review", reviewBridgeVerdict("approve"))
	assertEqual(t, "uncertain publication saved", f.view().Publication.State, "uncertain")
	for n := 0; n < 2; n++ {
		f.tool("publish_review", nil)
		assertEqual(t, "absence is not proof of failure", f.view().Publication.State, "uncertain")
	}
	assertEqual(t, "no blind second write", len(f.forge.Attempts()), 1)
}

func TestReviewBridgeAcceptanceCopiedMarkerStaysUncertain(t *testing.T) {
	for _, field := range []string{"identity", "revision", "event"} {
		t.Run(field, func(t *testing.T) {
			f := newReviewBridgeFixture(t)
			f.request(nil)
			f.accept()
			f.forge.Fail(forge.OpPublish, forge.Errorf(forge.ClassUncertain, "unknown"))
			f.tool("submit_review", reviewBridgeVerdict("approve"))
			view := f.view()
			r := forge.Receipt{PublicationReceipt: protocol.PublicationReceipt{ReviewID: "foreign-review", Identity: "fake-reviewer", Revision: reviewBridgePins().Head, Formal: true}, Event: "approve"}
			switch field {
			case "identity":
				r.Identity = "author"
			case "revision":
				r.Revision = strings.Repeat("d", 40)
			case "event":
				r.Event = "comment"
				r.Formal = false
			}
			f.forge.AddReview(reviewBridgeTarget(), r, forge.Marker("review-7", 1, view.Publication.Intent))
			f.tool("publish_review", nil)
			after := f.view()
			assertEqual(t, "forged receipt not adopted", after.Publication.Receipt == nil, true)
			assertEqual(t, "ambiguity remains visible", after.Publication.State, "uncertain")
			assertEqual(t, "ambiguity cannot retry write", len(f.forge.Attempts()), 1)
		})
	}
}

func (f *reviewBridgeFixture) reconnect() {
	f.t.Helper()
	f.client.Close()
	until := time.Now().Add(2 * time.Second)
	for {
		c, err := Dial(context.Background(), ClientConfig{SocketPath: f.agent, AgentID: "alpha", Forges: forge.Registry{"fake": f.forge}})
		if err == nil {
			f.client = c
			f.t.Cleanup(c.Close)
			f.mcp = connectMCP(f.t, c)
			return
		}
		if time.Now().After(until) {
			f.t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestReviewBridgeAcceptanceReconnectReconcilesPinnedPublishingIdentity(t *testing.T) {
	f := newReviewBridgeFixture(t)
	f.request(nil)
	f.accept()
	f.forge.LandThenFail(forge.Errorf(forge.ClassUncertain, "response lost"))
	f.tool("submit_review", reviewBridgeVerdict("approve"))
	before := f.view()
	f.forge.SetIdentity("different-account-after-reconnect")
	f.reconnect()
	f.tool("publish_review", nil)
	after := f.view()
	assertEqual(t, "reconnected bridge recovers existing receipt", after.Publication.State, "published")
	assertEqual(t, "original publishing identity retained", after.Publication.Receipt.Identity, "fake-reviewer")
	assertEqual(t, "same intent", after.Publication.Intent, before.Publication.Intent)
	assertEqual(t, "reconnect never resends", len(f.forge.Attempts()), 1)
}

type reviewBlockedPublish struct {
	forge.Forge
	entered chan struct{}
	release chan struct{}
}

func (f *reviewBlockedPublish) Publish(ctx context.Context, s forge.Submission) (forge.Receipt, error) {
	select {
	case f.entered <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
		return f.Forge.Publish(ctx, s)
	case <-ctx.Done():
		return forge.Receipt{}, forge.Errorf(forge.ClassUncertain, "interrupted")
	}
}

func TestReviewBridgeAcceptanceCancellationDuringWriteReconcilesActualOutcome(t *testing.T) {
	f := newReviewBridgeFixture(t)
	blocked := &reviewBlockedPublish{Forge: f.forge, entered: make(chan struct{}, 1), release: make(chan struct{})}
	f.client.forges["fake"] = blocked
	f.request(nil)
	f.accept()
	done := make(chan struct{})
	go func() { defer close(done); f.tool("submit_review", reviewBridgeVerdict("approve")) }()
	select {
	case <-blocked.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider write never started")
	}
	f.operatorCall(protocol.Message{Type: "operator_review", ReviewOp: "cancel", ReviewID: "review-7", ReviewReason: "stop review"})
	close(blocked.release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("provider response never reconciled")
	}
	got := f.view()
	assertEqual(t, "task remains cancelled", got.State, "cancelled")
	assertEqual(t, "actual provider success retained", got.Publication.State, "published")
	assertEqual(t, "provider review not claimed retracted", got.Publication.Receipt != nil, true)
	assertEqual(t, "only one provider write", len(f.forge.Attempts()), 1)
}

func TestReviewBridgeAcceptanceConcurrentRetryCannotIssueSecondProviderWrite(t *testing.T) {
	f := newReviewBridgeFixture(t)
	blocked := &reviewBlockedPublish{Forge: f.forge, entered: make(chan struct{}, 1), release: make(chan struct{})}
	f.client.forges["fake"] = blocked
	f.request(nil)
	f.accept()
	done := make(chan struct{})
	go func() { defer close(done); f.tool("submit_review", reviewBridgeVerdict("approve")) }()
	select {
	case <-blocked.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider write never started")
	}
	// While the original provider call is unresolved, Find may miss. That
	// cannot authorize another POST, even from a concurrent tool call.
	f.tool("publish_review", nil)
	close(blocked.release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("original provider call hung")
	}
	f.tool("publish_review", nil)
	assertEqual(t, "single POST under overlap", len(f.forge.Attempts()), 1)
	assertEqual(t, "published truth eventually reconciled", f.view().Publication.State, "published")
}
