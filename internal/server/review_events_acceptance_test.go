//go:build linux

package server

import (
	"os"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReviewAcceptanceOperatorEventRefreshMatchesAgentState(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	owner := sessionOpen(f)
	owner.snapshot()
	created := reviewTestView(t, reviewTestRequest(f, "live-review", reviewTestSpec()), "operator_review_result")
	event := sessionDecode[protocol.SessionEvent](t, owner.frame(), "event")
	operatorEqual(t, "operator receives change event", event.Kind, "reviews_changed")
	r, _ := owner.request("operator_review", map[string]any{"review_op": "status", "review_id": "live-review"})
	operatorEqual(t, "session and one-shot show same request", reflect.DeepEqual(reviewTestView(t, r, "operator_review_result"), created), true)
	ready := reviewTestView(t, reviewTestCall(p, "review_prepare", "live-review", map[string]any{"revisions": reviewTestRevisions()}), "review_result")
	event = sessionDecode[protocol.SessionEvent](t, owner.frame(), "event")
	operatorEqual(t, "reviewer transition notifies chat", event.Kind, "reviews_changed")
	r, _ = owner.request("operator_review", map[string]any{"review_op": "status", "review_id": "live-review"})
	operatorEqual(t, "chat refresh matches agent state", reflect.DeepEqual(reviewTestView(t, r, "operator_review_result"), ready), true)
}

func TestReviewAcceptanceFailedStoreWritePublishesNoTransition(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	reviewTestStart(f, p, "write-failure")
	owner := sessionOpen(f)
	before := owner.snapshot()
	inboxExec(f, `CREATE TRIGGER reject_review_update BEFORE UPDATE ON reviews BEGIN SELECT RAISE(ABORT,'unavailable'); END`)
	operatorEqual(t, "failed verdict write not confirmed", reviewTestCall(p, "review_submit", "write-failure", map[string]any{"attempt": 1, "verdict": reviewTestVerdict()}).text("type"), "error")
	operatorEqual(t, "failed write emits no event", owner.snapshot().Mark, before.Mark)
	got := reviewTestView(t, reviewTestCall(p, "review_get", "write-failure", nil), "review_result")
	operatorEqual(t, "original running state retained", got.State, "in_review")
	operatorEqual(t, "no partial result", got.Verdict == nil, true)
	inboxExec(f, `DROP TRIGGER reject_review_update`)
	reviewTestView(t, reviewTestCall(p, "review_submit", "write-failure", map[string]any{"attempt": 1, "verdict": reviewTestVerdict()}), "review_result")
	operatorEqual(t, "successful retry notifies operator", sessionDecode[protocol.SessionEvent](t, owner.frame(), "event").Kind, "reviews_changed")
}

func TestReviewAcceptanceReviewerAuditFailureRollsBackVerdict(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	reviewTestStart(f, p, "reviewer-audit")
	before := f.count("review_events")
	inboxExec(f, `CREATE TRIGGER reject_review_event BEFORE INSERT ON review_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	operatorEqual(t, "unaudited verdict refused", reviewTestCall(p, "review_submit", "reviewer-audit", map[string]any{"attempt": 1, "verdict": reviewTestVerdict()}).text("type"), "error")
	got := reviewTestView(t, reviewTestCall(p, "review_get", "reviewer-audit", nil), "review_result")
	operatorEqual(t, "verdict rollback", got.State, "in_review")
	operatorEqual(t, "no partial audit", f.count("review_events"), before)
	inboxExec(f, `DROP TRIGGER reject_review_event`)
	reviewTestView(t, reviewTestCall(p, "review_submit", "reviewer-audit", map[string]any{"attempt": 1, "verdict": reviewTestVerdict()}), "review_result")
	var actor, base, head, outcome string
	operatorNoError(t, f.db.QueryRow(`SELECT actor,base,head,outcome FROM review_events WHERE review_id='reviewer-audit' AND action='review_submit'`).Scan(&actor, &base, &head, &outcome))
	operatorEqual(t, "authenticated reviewer audited", actor, "alpha")
	operatorEqual(t, "audited base", base, reviewTestRevisions().Base)
	operatorEqual(t, "audited head", head, reviewTestRevisions().Head)
	operatorEqual(t, "audited outcome", outcome, "changes_requested")
	count := f.count("review_events")
	reviewTestView(t, reviewTestCall(p, "review_submit", "reviewer-audit", map[string]any{"attempt": 1, "verdict": reviewTestVerdict()}), "review_result")
	operatorEqual(t, "replayed verdict has one logical action", f.count("review_events"), count)
}
