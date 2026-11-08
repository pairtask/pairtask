package main

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestReviewIdentityCLIAcceptanceRemoteListAndStatusNameBothIDs(t *testing.T) {
	for _, op := range []string{"list", "status"} {
		t.Run(op, func(t *testing.T) {
			response := reviewCLIResponse()
			view := response["review"].(map[string]any)
			askID := `requester/ask"259`
			view["remote_request"] = askID
			view["remote"] = map[string]any{"contact": "sha256:peer", "sender": "requester"}
			if op == "list" {
				response = map[string]any{"type": "operator_review_result", "review_page": map[string]any{"reviews": []any{view}, "reviews_total": 1, "reviews_truncated": false}}
			}
			path, _ := roomCLISocket(t, response)
			args := []string{"operator", "review", op, "--socket", path}
			if op == "status" {
				args = append(args, "review-123")
			}
			var out, stderr bytes.Buffer
			assertStatusEqual(t, stderr.String(), run(context.Background(), args, &out, &stderr), 0)
			for _, want := range []string{strconv.Quote("review-123"), "requester's ask ID", strconv.Quote(askID)} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("%s output missing %q: %s", op, want, out.String())
				}
			}
		})
	}
}

func TestReviewIdentityCLIAcceptanceLocalReviewHasNoForeignAsk(t *testing.T) {
	path, _ := roomCLISocket(t, reviewCLIResponse())
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "review", "status", "--socket", path, "review-123"}, &out, &stderr)
	assertStatusEqual(t, stderr.String(), code, 0)
	assertStatusEqual(t, "local ID retained", strings.Contains(out.String(), strconv.Quote("review-123")), true)
	assertStatusEqual(t, "local review has no foreign mapping", strings.Contains(out.String(), "requester's ask ID"), false)
}
