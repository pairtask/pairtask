//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReviewAcceptanceEscapedVerdictCannotCreateUnreadableResult(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	reviewTestStart(f, p, "escaped-verdict")
	v := reviewTestVerdict()
	v.Findings = make([]protocol.Finding, 100)
	for i := range v.Findings {
		v.Findings[i] = protocol.Finding{Path: strings.Repeat("<", protocol.ReviewPathBytes), Line: 1, Summary: strings.Repeat("&", 1024)}
	}
	// Every field fits its individual bound, but escaping HTML expands the
	// verdict beyond its encoded-byte budget. Refuse before storing it.
	encoder := json.NewEncoder(p.Conn)
	encoder.SetEscapeHTML(false)
	operatorNoError(t, encoder.Encode(map[string]any{"type": "review_submit", "request_id": "expanded", "review_id": "escaped-verdict", "attempt": 1, "verdict": v}))
	response := p.frame()
	operatorEqual(t, "oversized output refused explicitly", response.text("type"), "error")
	got := reviewTestView(t, reviewTestCall(p, "review_get", "escaped-verdict", nil), "review_result")
	operatorEqual(t, "review stays usable", got.State, "in_review")
	operatorEqual(t, "unreadable result never persisted", got.Verdict == nil, true)
}

func TestReviewAcceptanceListBoundsSerializedBytesWithoutLosingDetails(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	reviewTestComplete(f, p, "list-seed")
	base := reviewTestView(t, reviewTestCall(p, "review_get", "list-seed", nil), "review_result")
	base.Verdict.Findings = make([]protocol.Finding, 32)
	for i := range base.Verdict.Findings {
		base.Verdict.Findings[i] = protocol.Finding{Path: "worker.go", Line: i + 1, Summary: strings.Repeat("<", 1024)}
	}
	// Seed completed history to test the output byte limit independently of
	// admission quotas and rate limits. Each individual review fits a frame.
	for i := 0; i < 20; i++ {
		base.ID = fmt.Sprintf("large-review-%02d", i)
		doc, err := json.Marshal(base)
		operatorNoError(t, err)
		_, err = f.db.Exec(`INSERT INTO reviews(review_id,reviewer,state,hash,token,doc,created_at,updated_at) SELECT ?,reviewer,state,hash,token,?,created_at,updated_at FROM reviews WHERE review_id='list-seed'`, base.ID, string(doc))
		operatorNoError(t, err)
	}
	r := reviewTestCall(p, "review_get", "", nil)
	operatorEqual(t, "byte-limited page remains readable", r.text("type"), "review_result")
	page := sessionDecode[protocol.ReviewPage](t, r, "review_page")
	operatorEqual(t, "full retained count", page.Total, 21)
	operatorEqual(t, "byte omissions explicit", page.Truncated, true)
	operatorEqual(t, "usable nonempty page", len(page.Reviews) > 0 && len(page.Reviews) < 21, true)
	full := reviewTestView(t, reviewTestCall(p, "review_get", "large-review-00", nil), "review_result")
	operatorEqual(t, "detail read retains every finding", len(full.Verdict.Findings), 32)
	operatorEqual(t, "detail text unmodified", full.Verdict.Findings[0].Summary, strings.Repeat("<", 1024))
}

func TestReviewAcceptanceInvalidRequestsNeverPersist(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"scope-bytes", "scope", strings.Repeat("é", 513)},
		{"no-reviewer", "reviewer", ""},
		{"operator-is-not-agent", "reviewer", "operator"},
		{"no-forge", "forge", ""},
		{"no-repository", "repository", ""},
		{"repository-bound", "repository", strings.Repeat("r", 257)},
		{"no-change", "change", ""},
		{"change-bound", "change", strings.Repeat("c", 65)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			reviewTestPeer(f, "alpha")
			spec := reviewTestSpec()
			spec[tc.field] = tc.value
			operatorEqual(t, "invalid request refused", reviewTestRequest(f, "invalid", spec).text("code"), "invalid_review")
			operatorEqual(t, "no partial request", f.count("reviews"), 0)
		})
	}
}

func TestReviewAcceptanceInvalidVerdictPreservesRunningAttempt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*protocol.Verdict)
	}{
		{"unknown-outcome", func(v *protocol.Verdict) { v.Outcome = "merge" }},
		{"summary-bytes", func(v *protocol.Verdict) { v.Summary = strings.Repeat("é", 2049) }},
		{"too-many-findings", func(v *protocol.Verdict) {
			v.Findings = make([]protocol.Finding, 101)
			for i := range v.Findings {
				v.Findings[i] = protocol.Finding{Path: "a.go", Line: 1, Summary: "finding"}
			}
		}},
		{"finding-bytes", func(v *protocol.Verdict) { v.Findings[0].Summary = strings.Repeat("é", 513) }},
		{"negative-line", func(v *protocol.Verdict) { v.Findings[0].Line = -1 }},
		{"missing-path", func(v *protocol.Verdict) { v.Findings[0].Path = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			p := reviewTestPeer(f, "alpha")
			reviewTestStart(f, p, "bad-verdict")
			v := reviewTestVerdict()
			tc.mutate(&v)
			operatorEqual(t, "malformed verdict refused", reviewTestCall(p, "review_submit", "bad-verdict", map[string]any{"attempt": 1, "verdict": v}).text("code"), "invalid_review")
			got := reviewTestView(t, reviewTestCall(p, "review_get", "bad-verdict", nil), "review_result")
			operatorEqual(t, "still in review", got.State, "in_review")
			operatorEqual(t, "no partial verdict", got.Verdict == nil, true)
		})
	}
}

func TestReviewAcceptancePerReviewerOpenBoundAndReadIsolation(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a, b := reviewTestPeer(f, "alpha"), reviewTestPeer(f, "beta")
	for i := 0; i < 8; i++ {
		reviewTestView(t, reviewTestRequest(f, fmt.Sprintf("open-%d", i), reviewTestSpec()), "operator_review_result")
	}
	operatorEqual(t, "ninth open request rejected", reviewTestRequest(f, "too-many", reviewTestSpec()).text("type"), "error")
	operatorEqual(t, "no overflow row", f.count("reviews"), 8)
	r := reviewTestCall(a, "review_get", "", nil)
	operatorEqual(t, "owner can list", r.text("type"), "review_result")
	operatorEqual(t, "all open reviews visible", sessionDecode[protocol.ReviewPage](t, r, "review_page").Total, 8)
	r = reviewTestCall(b, "review_get", "", nil)
	operatorEqual(t, "other reviewer list works", r.text("type"), "review_result")
	operatorEqual(t, "other reviewer learns no reviews", sessionDecode[protocol.ReviewPage](t, r, "review_page").Total, 0)
	reviewTestView(t, reviewTestCall(a, "review_decline", "open-0", map[string]any{"review_reason": "outside expertise"}), "review_result")
	reviewTestView(t, reviewTestRequest(f, "replacement", reviewTestSpec()), "operator_review_result")
}

func TestReviewAcceptanceAuditFailurePreventsAdmission(t *testing.T) {
	for _, event := range []string{"INSERT", "UPDATE"} {
		t.Run(event, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			reviewTestPeer(f, "alpha")
			inboxExec(f, "CREATE TRIGGER fail_review_audit BEFORE "+event+" ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END")
			operatorEqual(t, "audit failure denies admission", reviewTestRequest(f, "not-audited", reviewTestSpec()).text("type"), "error")
			operatorEqual(t, "failed audit creates no review", f.count("reviews"), 0)
		})
	}
}

func TestReviewAcceptanceAgentCannotCreateOperatorRequest(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	p.send(map[string]any{"type": "operator_review", "request_id": "forged", "review_op": "request", "message_id": "forged-review", "review_spec": reviewTestSpec()})
	operatorEqual(t, "agent cannot use operator control plane", p.frame().text("type"), "error")
	operatorEqual(t, "forgery leaves no review", f.count("reviews"), 0)
}

func TestReviewAcceptanceFiniteAttemptsAndProviderWriteIntents(t *testing.T) {
	t.Run("analysis", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), "")
		p := reviewTestPeer(f, "alpha")
		reviewTestStart(f, p, "bounded-attempts")
		for attempt := 1; attempt < protocol.ReviewAttempts; attempt++ {
			got := reviewTestView(t, reviewTestCall(p, "review_restart", "bounded-attempts", map[string]any{"attempt": attempt}), "review_result")
			operatorEqual(t, "restart increments once", got.Attempt, attempt+1)
		}
		operatorEqual(t, "restart limit enforced", reviewTestCall(p, "review_restart", "bounded-attempts", map[string]any{"attempt": protocol.ReviewAttempts}).text("code"), "invalid_review")
		got := reviewTestView(t, reviewTestCall(p, "review_get", "bounded-attempts", nil), "review_result")
		operatorEqual(t, "refused restart preserves active attempt", got.Attempt, protocol.ReviewAttempts)
	})
	t.Run("publication", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), "")
		p := reviewTestPeer(f, "alpha")
		reviewTestComplete(f, p, "bounded-writes")
		// Seed prior failed attempts so the finite per-task bound is tested
		// independently of the connection's burst-rate limit.
		_, err := f.db.Exec(`UPDATE reviews SET doc=json_set(doc,'$.publication.intent',?,'$.publication.state','failed') WHERE review_id='bounded-writes'`, protocol.ReviewWriteIntents-1)
		operatorNoError(t, err)
		got := reviewTestView(t, reviewTestPublication(p, "bounded-writes", "failed", "submitting", map[string]any{"token": "last-allowed-write"}), "review_result")
		operatorEqual(t, "last permitted intent", got.Publication.Intent, protocol.ReviewWriteIntents)
		reviewTestView(t, reviewTestPublication(p, "bounded-writes", "submitting", "failed", map[string]any{"intent": got.Publication.Intent, "reason": "forbidden"}), "review_result")
		operatorEqual(t, "write limit enforced", reviewTestPublication(p, "bounded-writes", "failed", "submitting", map[string]any{"token": "one-too-many"}).text("code"), "invalid_review")
		got = reviewTestView(t, reviewTestCall(p, "review_get", "bounded-writes", nil), "review_result")
		operatorEqual(t, "verdict remains available at limit", got.Verdict != nil, true)
		operatorEqual(t, "no extra intent created", got.Publication.Intent, protocol.ReviewWriteIntents)
	})
}
