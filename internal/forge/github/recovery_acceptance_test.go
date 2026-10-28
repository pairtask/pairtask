package github_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pairtask/pairtask/internal/forge"
)

func TestGitHubAcceptanceFindAllPagesAndRejectsDuplicateMarkers(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprint(duplicate), func(t *testing.T) {
			var calls atomic.Int32
			expected := ghExpected()
			f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				ghEqual(t, "find method", r.Method, "GET")
				ghEqual(t, "find path", r.URL.Path, "/repos/owner/repo/pulls/42/reviews")
				ghEqual(t, "page size", r.URL.Query().Get("per_page"), "100")
				if r.URL.Query().Get("page") != "2" {
					w.Header().Set("Link", fmt.Sprintf("<http://%s%s?per_page=100&page=2>; rel=\"next\"", r.Host, r.URL.Path))
					ghJSON(w, []any{ghReceipt("APPROVED", expected.Marker)})
					return
				}
				if duplicate {
					row := ghReceipt("APPROVED", expected.Marker)
					row["id"] = 124
					ghJSON(w, []any{row})
				} else {
					ghJSON(w, []any{})
				}
			})
			receipt, found, err := f.Find(context.Background(), fixtureTarget, expected)
			ghEqual(t, "all pages read", calls.Load(), 2)
			if duplicate {
				ghClass(t, err, forge.ClassAmbiguous)
				ghEqual(t, "no ambiguous match", found, false)
			} else {
				ghOK(t, err)
				ghEqual(t, "found", found, true)
				ghEqual(t, "receipt", receipt.ReviewID, "123")
			}
		})
	}
}

func TestGitHubAcceptanceCopiedMarkerCannotClaimPublication(t *testing.T) {
	for _, field := range []string{"identity", "revision", "event", "pending", "dismissed"} {
		t.Run(field, func(t *testing.T) {
			e := ghExpected()
			row := ghReceipt("APPROVED", e.Marker)
			switch field {
			case "identity":
				row["user"] = map[string]string{"login": "someone-else"}
			case "revision":
				row["commit_id"] = strings.Repeat("c", 40)
			case "event":
				row["state"] = "COMMENTED"
			case "pending":
				row["state"] = "PENDING"
			case "dismissed":
				row["state"] = "DISMISSED"
			}
			f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) { ghJSON(w, []any{row}) })
			_, found, err := f.Find(context.Background(), fixtureTarget, e)
			if err == nil {
				t.Fatal("mismatched marker must fail reconciliation")
			}
			ghEqual(t, "not found as ours", found, false)
		})
	}
}

func TestGitHubAcceptanceFindCapCannotProveAbsence(t *testing.T) {
	var calls atomic.Int32
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Link", fmt.Sprintf("<http://%s%s?per_page=100&page=%d>; rel=\"next\"", r.Host, r.URL.Path, calls.Load()+1))
		ghJSON(w, []any{})
	})
	_, found, err := f.Find(context.Background(), fixtureTarget, ghExpected())
	ghClass(t, err, forge.ClassUnavailable)
	ghEqual(t, "no false absence success", found, false)
	ghEqual(t, "bounded page count", calls.Load(), 2)
}

func TestGitHubAcceptanceLostPostResponseReconcilesWithoutSecondPost(t *testing.T) {
	var posts atomic.Int32
	e := ghExpected()
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		ghJSON(w, []any{ghReceipt("APPROVED", e.Marker)})
	})
	_, err := f.Publish(context.Background(), ghSubmission(forge.EventApprove))
	ghClass(t, err, forge.ClassUncertain)
	receipt, found, err := f.Find(context.Background(), fixtureTarget, e)
	ghOK(t, err)
	ghEqual(t, "reconciled", found, true)
	ghEqual(t, "receipt", receipt.ReviewID, "123")
	ghEqual(t, "exactly one attempted POST", posts.Load(), 1)
}

func TestGitHubAcceptanceMalformedSuccessRemainsUncertain(t *testing.T) {
	for _, kind := range []string{"json", "missing id", "missing identity", "pending", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
				row := ghReceipt("APPROVED", ghExpected().Marker)
				switch kind {
				case "json":
					fmt.Fprint(w, "{")
					return
				case "missing id":
					delete(row, "id")
				case "missing identity":
					delete(row, "user")
				case "pending":
					row["state"] = "PENDING"
				case "oversized":
					fmt.Fprint(w, strings.Repeat("x", (1<<20)+1))
					return
				}
				ghJSON(w, row)
			})
			_, err := f.Publish(context.Background(), ghSubmission(forge.EventApprove))
			ghClass(t, err, forge.ClassUncertain)
		})
	}
}

func TestGitHubAcceptanceDefiniteRefusalsAndServerUncertainty(t *testing.T) {
	for _, tc := range []struct {
		code                int
		header, body, class string
	}{
		{401, "", "bad credentials", forge.ClassForbidden}, {403, "", "forbidden", forge.ClassForbidden}, {403, "retry", "slow down", forge.ClassRateLimited}, {403, "zero", "rate limit", forge.ClassRateLimited}, {404, "", "not found", forge.ClassNotFound}, {429, "", "slow down", forge.ClassRateLimited}, {422, "", "Can not approve your own pull request", forge.ClassSelfReview}, {422, "", "invalid line", forge.ClassInvalid}, {503, "", "unavailable", forge.ClassUncertain},
	} {
		t.Run(fmt.Sprintf("%d-%s", tc.code, tc.header), func(t *testing.T) {
			f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.header == "retry" {
					w.Header().Set("Retry-After", "30")
				}
				if tc.header == "zero" {
					w.Header().Set("X-RateLimit-Remaining", "0")
				}
				w.WriteHeader(tc.code)
				ghJSON(w, map[string]string{"message": tc.body + " " + fixtureToken})
			})
			_, err := f.Publish(context.Background(), ghSubmission(forge.EventApprove))
			ghClass(t, err, tc.class)
		})
	}
}
