//go:build linux

package server

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Exercise the public transport and UID boundary. The request maps deliberately
// keep omitted fields absent, so defaulting is tested at the wire boundary.
func reviewTestSpec() map[string]any {
	return map[string]any{"forge": "github", "repository": "github.com/team/project", "change": "201", "reviewer": "alpha", "scope": "Check cancellation and retry behavior"}
}

func reviewTestOperator(f *operatorFixture, fields map[string]any) sessionWire {
	f.t.Helper()
	p := &sessionProbe{operatorPeer: operatorDial(f.t, f.operator)}
	defer p.Close()
	fields["type"] = "operator_review"
	p.send(fields)
	return p.frame()
}

func reviewTestRequest(f *operatorFixture, id string, spec map[string]any) sessionWire {
	f.t.Helper()
	return reviewTestOperator(f, map[string]any{"review_op": "request", "message_id": id, "review_spec": spec})
}

func reviewTestView(t *testing.T, r sessionWire, kind string) protocol.ReviewView {
	t.Helper()
	if r.text("type") != kind {
		t.Fatalf("expected %s: %s", kind, sessionJSON(r))
	}
	return sessionDecode[protocol.ReviewView](t, r, "review")
}

func reviewTestPeer(f *operatorFixture, id string) *sessionProbe {
	return &sessionProbe{operatorPeer: f.peer(id, protocol.ReviewVersion)}
}

func reviewTestCall(p *sessionProbe, kind, id string, fields map[string]any) sessionWire {
	p.t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["review_id"] = id
	r, events := p.request(kind, fields)
	operatorEqual(p.t, "review changes are not unsolicited agent messages", len(events), 0)
	return r
}

func reviewTestRevisions() protocol.Revisions {
	return protocol.Revisions{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
}

func reviewTestStart(f *operatorFixture, p *sessionProbe, id string) protocol.ReviewView {
	f.t.Helper()
	reviewTestView(f.t, reviewTestRequest(f, id, reviewTestSpec()), "operator_review_result")
	reviewTestView(f.t, reviewTestCall(p, "review_prepare", id, map[string]any{"revisions": reviewTestRevisions()}), "review_result")
	return reviewTestView(f.t, reviewTestCall(p, "review_accept", id, nil), "review_result")
}

func reviewTestVerdict() protocol.Verdict {
	return protocol.Verdict{Outcome: "changes_requested", Summary: "Cancellation permits a late result", Findings: []protocol.Finding{{Path: "worker.go", Line: 7, Summary: "Check the active attempt before saving"}}}
}

func TestReviewAcceptanceRequestDefaultsAndConflict(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	reviewTestPeer(f, "alpha")
	spec := reviewTestSpec()
	first := reviewTestView(t, reviewTestRequest(f, "stable-request", spec), "operator_review_result")
	operatorEqual(t, "durable identity", first.ID, "stable-request")
	operatorEqual(t, "initial state", first.State, "requested")
	operatorEqual(t, "no work before pinning", first.Attempt, 0)
	operatorEqual(t, "unresolved revisions explicit", first.Revisions == nil, true)
	operatorEqual(t, "authenticated requester recorded", first.Requester != "", true)
	// Inspect the public policy independently of Go pointer/value representation.
	wire := reviewTestRequest(f, "stable-request", spec)
	view := sessionDecode[map[string]any](t, wire, "review")
	policy := view["spec"].(map[string]any)["policy"].(map[string]any)
	operatorEqual(t, "publish by default", policy["publish"], any(true))
	operatorEqual(t, "formal by default", policy["formal"], any(true))
	again := reviewTestView(t, wire, "operator_review_result")
	operatorEqual(t, "retry preserves admission time", again.CreatedAt, first.CreatedAt)
	spec["scope"] = "different work"
	operatorEqual(t, "conflicting replay rejected", reviewTestRequest(f, "stable-request", spec).text("code"), "review_conflict")
	operatorEqual(t, "one durable request", f.count("reviews"), 1)
}

func TestReviewAcceptancePinningAndRestartFenceOldAttempt(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	reviewTestView(t, reviewTestRequest(f, "pinned", reviewTestSpec()), "operator_review_result")
	operatorEqual(t, "cannot start unresolved work", reviewTestCall(p, "review_accept", "pinned", nil).text("code"), "review_conflict")
	pins := reviewTestRevisions()
	ready := reviewTestView(t, reviewTestCall(p, "review_prepare", "pinned", map[string]any{"revisions": pins}), "review_result")
	operatorEqual(t, "ready is not running", ready.State, "ready")
	operatorEqual(t, "ready attempt", ready.Attempt, 0)
	reviewTestView(t, reviewTestCall(p, "review_prepare", "pinned", map[string]any{"revisions": pins}), "review_result")
	moved := pins
	moved.Head = strings.Repeat("c", 40)
	operatorEqual(t, "cannot repin", reviewTestCall(p, "review_prepare", "pinned", map[string]any{"revisions": moved}).text("code"), "review_conflict")
	started := reviewTestView(t, reviewTestCall(p, "review_accept", "pinned", nil), "review_result")
	operatorEqual(t, "first attempt", started.Attempt, 1)
	restarted := reviewTestView(t, reviewTestCall(p, "review_restart", "pinned", map[string]any{"attempt": 1}), "review_result")
	operatorEqual(t, "new attempt", restarted.Attempt, 2)
	operatorEqual(t, "restart preserves revisions", *restarted.Revisions, pins)
	operatorEqual(t, "restart CAS", reviewTestCall(p, "review_restart", "pinned", map[string]any{"attempt": 1}).text("code"), "review_conflict")
	operatorEqual(t, "old attempt cannot finish", reviewTestCall(p, "review_submit", "pinned", map[string]any{"attempt": 1, "verdict": reviewTestVerdict()}).text("code"), "review_conflict")
	done := reviewTestView(t, reviewTestCall(p, "review_submit", "pinned", map[string]any{"attempt": 2, "verdict": reviewTestVerdict()}), "review_result")
	operatorEqual(t, "completed analysis", done.State, "completed")
	operatorEqual(t, "publication distinct", done.Publication.State, "pending")
}

func TestReviewAcceptanceWrongAgentCannotReadOrMutate(t *testing.T) {
	for _, kind := range []string{"review_get", "review_prepare", "review_accept", "review_decline", "review_restart", "review_submit", "review_publication"} {
		t.Run(kind, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			a, b := reviewTestPeer(f, "alpha"), reviewTestPeer(f, "beta")
			before := reviewTestStart(f, a, "private-review")
			r := reviewTestCall(b, kind, "private-review", map[string]any{"attempt": 1, "revisions": reviewTestRevisions(), "verdict": reviewTestVerdict(), "reviewer": "alpha", "review_reason": "decline", "publication": map[string]any{"from": "pending", "to": "submitting"}})
			operatorEqual(t, "wrong connection denied", r.text("type"), "error")
			operatorEqual(t, "no private result leaked", len(r["review"]), 0)
			after := reviewTestView(t, reviewTestCall(a, "review_get", "private-review", nil), "review_result")
			operatorEqual(t, "unauthorized request has no effect", reflect.DeepEqual(before, after), true)
		})
	}
}

func TestReviewAcceptanceCancellationFencesLateVerdict(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	reviewTestStart(f, p, "cancelled-work")
	r := reviewTestOperator(f, map[string]any{"review_op": "cancel", "review_id": "cancelled-work", "review_reason": "operator stopped this review"})
	operatorEqual(t, "cancelled", reviewTestView(t, r, "operator_review_result").State, "cancelled")
	for _, kind := range []string{"review_accept", "review_restart", "review_submit"} {
		operatorEqual(t, "cancel fences "+kind, reviewTestCall(p, kind, "cancelled-work", map[string]any{"attempt": 1, "verdict": reviewTestVerdict()}).text("code"), "review_conflict")
	}
	got := reviewTestView(t, reviewTestCall(p, "review_get", "cancelled-work", nil), "review_result")
	operatorEqual(t, "late verdict not retained", got.Verdict == nil, true)
}

func TestReviewAcceptanceExplicitChatOnlyPolicy(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	spec := reviewTestSpec()
	spec["policy"] = map[string]bool{"publish": false, "formal": false}
	reviewTestView(t, reviewTestRequest(f, "chat-only", spec), "operator_review_result")
	reviewTestView(t, reviewTestCall(p, "review_prepare", "chat-only", map[string]any{"revisions": reviewTestRevisions()}), "review_result")
	reviewTestView(t, reviewTestCall(p, "review_accept", "chat-only", nil), "review_result")
	done := reviewTestView(t, reviewTestCall(p, "review_submit", "chat-only", map[string]any{"attempt": 1, "verdict": reviewTestVerdict()}), "review_result")
	operatorEqual(t, "chat-only is never pending publication", done.Publication.State, "none")
	operatorEqual(t, "chat result retained", reflect.DeepEqual(*done.Verdict, reviewTestVerdict()), true)
	operatorEqual(t, "policy cannot be bypassed", reviewTestCall(p, "review_publication", "chat-only", map[string]any{"attempt": 1, "publication": map[string]any{"from": "none", "to": "submitting"}}).text("type"), "error")
}

func TestReviewAcceptanceRestartRetainsTaskPinsAndVerdict(t *testing.T) {
	database := filepath.Join(t.TempDir(), "reviews.db")
	var started protocol.ReviewView
	t.Run("admission", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		started = reviewTestStart(f, reviewTestPeer(f, "alpha"), "persisted")
	})
	if t.Failed() {
		return
	}
	t.Run("resume-and-finish", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := reviewTestPeer(f, "alpha")
		got := reviewTestView(t, reviewTestCall(p, "review_get", "persisted", nil), "review_result")
		operatorEqual(t, "task survives broker restart", reflect.DeepEqual(got, started), true)
		reviewTestView(t, reviewTestCall(p, "review_submit", "persisted", map[string]any{"attempt": 1, "verdict": reviewTestVerdict()}), "review_result")
	})
	if t.Failed() {
		return
	}
	t.Run("recover-result", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := reviewTestPeer(f, "alpha")
		got := reviewTestView(t, reviewTestCall(p, "review_get", "persisted", nil), "review_result")
		operatorEqual(t, "completion durable", got.State, "completed")
		operatorEqual(t, "publication still pending", got.Publication.State, "pending")
		operatorEqual(t, "findings durable", reflect.DeepEqual(*got.Verdict, reviewTestVerdict()), true)
		operatorEqual(t, "one task after restarts", f.count("reviews"), 1)
	})
}
