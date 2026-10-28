//go:build linux

package bridge

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReviewBridgeAcceptancePublishesEachVerdictAtPinnedRevision(t *testing.T) {
	for _, outcome := range []string{"approve", "changes_requested", "comment"} {
		t.Run(outcome, func(t *testing.T) {
			f := newReviewBridgeFixture(t)
			f.request(nil)
			f.accept()
			result := f.tool("submit_review", reviewBridgeVerdict(outcome))
			if result.IsError {
				t.Fatalf("submit: %+v", result.Content)
			}
			got := f.view()
			assertEqual(t, "analysis complete", got.State, "completed")
			assertEqual(t, "provider publication confirmed", got.Publication.State, "published")
			assertEqual(t, "verdict preserved", got.Verdict.Outcome, outcome)
			assertEqual(t, "exact pins", *got.Revisions, reviewBridgePins())
			writes := f.forge.Attempts()
			assertEqual(t, "one provider write", len(writes), 1)
			assertEqual(t, "provider target", writes[0].Target, reviewBridgeTarget())
			assertEqual(t, "provider exact pins", writes[0].Pinned, reviewBridgePins())
			assertEqual(t, "provider event", writes[0].Event, outcome)
			assertEqual(t, "publisher fixed before write", writes[0].Identity, "fake-reviewer")
			assertEqual(t, "receipt actual account", got.Publication.Receipt.Identity, "fake-reviewer")
			assertEqual(t, "receipt exact head", got.Publication.Receipt.Revision, reviewBridgePins().Head)
			assertEqual(t, "attempt-specific marker", writes[0].Marker, forge.Marker("review-7", 1, got.Publication.Intent))
			f.tool("publish_review", nil)
			assertEqual(t, "published retry makes no write", len(f.forge.Attempts()), 1)
		})
	}
}

func TestReviewBridgeAcceptanceRepeatedAcceptNeverRepinsMovedHead(t *testing.T) {
	f := newReviewBridgeFixture(t)
	f.request(nil)
	f.accept()
	before := f.view()
	moved := reviewBridgePins()
	moved.Head = strings.Repeat("c", 40)
	f.forge.SetRevisions(reviewBridgeTarget(), moved)
	f.accept()
	assertEqual(t, "accept retry preserves entire running task", reflect.DeepEqual(f.view(), before), true)
}

func TestReviewBridgeAcceptanceFindingsMustBelongToPinnedDiff(t *testing.T) {
	for _, finding := range []protocol.Finding{
		{Path: "unrelated.go", Line: 5, Summary: "outside diff"},
		{Path: "worker.go", Line: 8, Summary: "outside changed lines"},
	} {
		t.Run(finding.Summary, func(t *testing.T) {
			f := newReviewBridgeFixture(t)
			f.request(nil)
			f.accept()
			args := reviewBridgeVerdict("approve")
			args["findings"] = []protocol.Finding{finding}
			assertEqual(t, "invalid finding refused", f.tool("submit_review", args).IsError, true)
			got := f.view()
			assertEqual(t, "no invalid verdict saved", got.Verdict == nil, true)
			assertEqual(t, "review remains running", got.State, "in_review")
			assertEqual(t, "no provider write", len(f.forge.Attempts()), 0)
		})
	}
}

func TestReviewBridgeAcceptanceEitherRevisionDriftPreservesVerdictWithoutPublishing(t *testing.T) {
	for _, field := range []string{"base", "head"} {
		t.Run(field, func(t *testing.T) {
			f := newReviewBridgeFixture(t)
			f.request(nil)
			f.accept()
			moved := reviewBridgePins()
			if field == "base" {
				moved.Base = strings.Repeat("c", 40)
			} else {
				moved.Head = strings.Repeat("c", 40)
			}
			f.forge.SetRevisions(reviewBridgeTarget(), moved)
			f.tool("submit_review", reviewBridgeVerdict("approve"))
			got := f.view()
			assertEqual(t, "analysis result retained", got.State, "completed")
			assertEqual(t, "review still describes original pins", *got.Revisions, reviewBridgePins())
			assertEqual(t, "moved revisions exposed", *got.Stale, moved)
			assertEqual(t, "no stale approval", len(f.forge.Attempts()), 0)
		})
	}
}

func TestReviewBridgeAcceptanceUnsupportedFormalReviewNeverDowngrades(t *testing.T) {
	for _, outcome := range []string{"approve", "changes_requested"} {
		t.Run(outcome, func(t *testing.T) {
			f := newReviewBridgeFixture(t)
			f.forge.SetCapabilities(forge.Caps{LineComments: true, Reconcile: true})
			f.request(nil)
			f.accept()
			f.tool("submit_review", reviewBridgeVerdict(outcome))
			got := f.view()
			assertEqual(t, "verdict remains available", got.State, "completed")
			assertEqual(t, "no silent downgrade", len(f.forge.Attempts()), 0)
			assertEqual(t, "not reported published", got.Publication.State == "published", false)
		})
	}
}

func TestReviewBridgeAcceptanceExplicitPoliciesControlPublication(t *testing.T) {
	for _, publish := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat-only", true: "comment-only"}[publish], func(t *testing.T) {
			f := newReviewBridgeFixture(t)
			f.request(&protocol.ReviewPolicy{Publish: publish, Formal: false})
			f.accept()
			f.tool("submit_review", reviewBridgeVerdict("approve"))
			got := f.view()
			assertEqual(t, "saved analysis outcome unchanged", got.Verdict.Outcome, "approve")
			writes := f.forge.Attempts()
			if !publish {
				assertEqual(t, "no publication", len(writes), 0)
				return
			}
			assertEqual(t, "explicit comment posted", len(writes), 1)
			assertEqual(t, "comment event", writes[0].Event, "comment")
			assertEqual(t, "not reported formal approval", got.Publication.Receipt.Formal, false)
		})
	}
}

func TestReviewBridgeAcceptanceKnownFailureRetriesSavedVerdict(t *testing.T) {
	for _, class := range []string{forge.ClassForbidden, forge.ClassSelfReview, forge.ClassRateLimited} {
		t.Run(class, func(t *testing.T) {
			f := newReviewBridgeFixture(t)
			f.request(nil)
			f.accept()
			f.forge.Fail(forge.OpPublish, forge.Errorf(class, "provider refused"))
			f.tool("submit_review", reviewBridgeVerdict("approve"))
			before := f.view()
			assertEqual(t, "failed publication visible", before.Publication.State, "failed")
			assertEqual(t, "known failure retained", before.Publication.Reason, class)
			// A retry must not rerun diff validation or analysis.
			f.forge.Fail(forge.OpChanges, forge.Errorf(forge.ClassUnavailable, "analysis must not run again"))
			f.tool("publish_review", nil)
			after := f.view()
			assertEqual(t, "retry publishes", after.Publication.State, "published")
			assertEqual(t, "same analysis attempt", after.Attempt, before.Attempt)
			assertEqual(t, "same saved verdict", reflect.DeepEqual(after.Verdict, before.Verdict), true)
			assertEqual(t, "exactly one retry", len(f.forge.Attempts()), 2)
		})
	}
}

func TestReviewBridgeAcceptanceLostResponseReconcilesWithoutResending(t *testing.T) {
	f := newReviewBridgeFixture(t)
	f.request(nil)
	f.accept()
	f.forge.LandThenFail(forge.Errorf(forge.ClassUncertain, "lost response"))
	f.tool("submit_review", reviewBridgeVerdict("approve"))
	assertEqual(t, "uncertainty explicit", f.view().Publication.State, "uncertain")
	assertEqual(t, "write actually landed", len(f.forge.Reviews(reviewBridgeTarget())), 1)
	f.tool("publish_review", nil)
	assertEqual(t, "found receipt reconciled", f.view().Publication.State, "published")
	assertEqual(t, "no repeated POST", len(f.forge.Attempts()), 1)
}
