package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func reviewCLIResponse() map[string]any {
	return map[string]any{"type": "operator_review_result", "review": map[string]any{
		"review_id": "review-123", "state": "requested", "attempt": 0,
		"spec": map[string]any{"forge": "github", "repository": "github.com/team/project", "change": "201", "reviewer": "alpha", "policy": map[string]bool{"publish": true, "formal": true}},
	}}
}

func TestReviewCLIAcceptanceRequestPreservesTargetScopeAndPolicy(t *testing.T) {
	for _, target := range []string{"https://github.com/team/project/pull/201", "team/project#201"} {
		t.Run(target, func(t *testing.T) {
			path, requests := roomCLISocket(t, reviewCLIResponse())
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"operator", "review", "request", "--socket", path, "--json", "--reviewer", "alpha", "--scope", "Check auth; do not change code", target}, &out, &stderr)
			assertStatusEqual(t, "review request: "+stderr.String(), code, 0)
			req := roomCLIRequest(t, requests)
			assertStatusEqual(t, "operator endpoint", roomCLIString(req, "type"), "operator_review")
			assertStatusEqual(t, "request operation", roomCLIString(req, "review_op"), "request")
			assertStatusEqual(t, "durable request identity", roomCLIString(req, "message_id") != "", true)
			spec, ok := req["review_spec"].(map[string]any)
			if !ok {
				t.Fatal("missing structured review specification")
			}
			for k, want := range map[string]string{"forge": "github", "repository": "github.com/team/project", "change": "201", "reviewer": "alpha", "scope": "Check auth; do not change code"} {
				assertStatusEqual(t, k, roomCLIString(spec, k), want)
			}
			if policy, ok := spec["policy"].(map[string]any); ok {
				assertStatusEqual(t, "default publish", policy["publish"], any(true))
				assertStatusEqual(t, "default formal", policy["formal"], any(true))
			}
			var got map[string]any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["review"] == nil {
				t.Fatal("JSON lost review result")
			}
		})
	}
}

func TestReviewCLIAcceptanceListStatusCancelAndChatOnly(t *testing.T) {
	for _, tc := range []struct {
		op, id string
		args   []string
	}{
		{"list", "", nil},
		{"status", "review-123", []string{"review-123"}},
		{"cancel", "review-123", []string{"review-123"}},
		{"request", "", []string{"--reviewer", "alpha", "--no-publish", "--no-formal", "team/project#201"}},
	} {
		t.Run(tc.op, func(t *testing.T) {
			response := reviewCLIResponse()
			if tc.op == "list" {
				response = map[string]any{"type": "operator_review_result", "review_page": map[string]any{"reviews": []any{}, "reviews_total": 0, "reviews_truncated": false}}
			}
			path, requests := roomCLISocket(t, response)
			args := append([]string{"operator", "review", tc.op, "--socket", path, "--json"}, tc.args...)
			var out, stderr bytes.Buffer
			assertStatusEqual(t, "review operation: "+stderr.String(), run(context.Background(), args, &out, &stderr), 0)
			req := roomCLIRequest(t, requests)
			assertStatusEqual(t, "operation", roomCLIString(req, "review_op"), tc.op)
			assertStatusEqual(t, "review identity", roomCLIString(req, "review_id"), tc.id)
			if tc.op == "request" {
				spec := req["review_spec"].(map[string]any)
				policy := spec["policy"].(map[string]any)
				assertStatusEqual(t, "explicit no publication", policy["publish"], any(false))
				assertStatusEqual(t, "explicit no formal approval", policy["formal"], any(false))
			}
		})
	}
}

func TestReviewCLIAcceptanceBrokerFailureNeverReportsSuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reply  map[string]any
		reason string
	}{
		{"older-broker", map[string]any{"type": "error", "code": "unsupported_operation", "detail": "review requests are unsupported"}, "unsupported"},
		{"wrong-response", map[string]any{"type": "operator_room_result"}, ""},
		{"missing-result", map[string]any{"type": "operator_review_result"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := roomCLISocket(t, tc.reply)
			var out, stderr bytes.Buffer
			code := run(context.Background(), []string{"operator", "review", "status", "--socket", path, "review-123"}, &out, &stderr)
			assertStatusEqual(t, "failure exit", code != 0, true)
			assertStatusEqual(t, "failure explained", stderr.Len() > 0, true)
			if tc.reason != "" {
				assertStatusEqual(t, "provider reason retained", strings.Contains(stderr.String(), tc.reason), true)
			}
		})
	}
}

func TestReviewCLIAcceptanceExplicitRequestIDSupportsRetry(t *testing.T) {
	path, requests := roomCLISocket(t, reviewCLIResponse())
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "review", "request", "--socket", path, "--message-id", "retryable-review", "--reviewer", "alpha", "team/project#201"}, &out, &stderr)
	assertStatusEqual(t, "explicit retry identity: "+stderr.String(), code, 0)
	assertStatusEqual(t, "caller identity retained", roomCLIString(roomCLIRequest(t, requests), "message_id"), "retryable-review")
}

func TestReviewCLIAcceptanceStatusShowsFindingsAndPublicationFailure(t *testing.T) {
	response := reviewCLIResponse()
	view := response["review"].(map[string]any)
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	view["state"] = "completed"
	view["revisions"] = map[string]string{"base": base, "head": head}
	view["verdict"] = map[string]any{"outcome": "changes_requested", "summary": "Retry can duplicate a provider write", "findings": []any{map[string]any{"path": "worker.go", "line": 27, "summary": "Reconcile before another POST"}}}
	view["publication"] = map[string]any{"state": "failed", "reason": "forbidden"}
	path, _ := roomCLISocket(t, response)
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "review", "status", "--socket", path, "review-123"}, &out, &stderr)
	assertStatusEqual(t, "read completed review: "+stderr.String(), code, 0)
	for _, want := range []string{base, head, "Retry can duplicate a provider write", "worker.go", "27", "Reconcile before another POST", "failed", "forbidden"} {
		assertStatusEqual(t, "review status includes "+want, strings.Contains(out.String(), want), true)
	}
}
