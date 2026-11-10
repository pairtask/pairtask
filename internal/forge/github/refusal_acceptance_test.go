package github_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/pairtask/pairtask/internal/forge"
	gh "github.com/pairtask/pairtask/internal/forge/github"
)

// A refused lookup says which refusal: GitHub rejecting the token (401) is
// not the token being forbidden this repository (403), and a rate limit
// answered as 403 is no refusal at all.
func TestGitHubRefusalAcceptanceStatusSaysWhichRefusal(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		header, value string
		class         string
		reason        forge.Reason
	}{
		{"401", 401, "", "", forge.ClassForbidden, forge.ReasonTokenRejected},
		{"403", 403, "", "", forge.ClassForbidden, forge.ReasonRepository},
		{"403-quota-left", 403, "X-RateLimit-Remaining", "4999", forge.ClassForbidden, forge.ReasonRepository},
		{"403-retry-after", 403, "Retry-After", "30", forge.ClassRateLimited, ""},
		{"403-quota-spent", 403, "X-RateLimit-Remaining", "0", forge.ClassRateLimited, ""},
		{"404", 404, "", "", forge.ClassNotFound, ""},
		{"429", 429, "", "", forge.ClassRateLimited, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.header != "" {
					w.Header().Set(tc.header, tc.value)
				}
				w.WriteHeader(tc.status)
				ghJSON(w, map[string]string{"message": "GitHub's own words " + fixtureToken})
			})
			_, err := f.Resolve(context.Background(), fixtureTarget)
			ghClass(t, err, tc.class)
			ghEqual(t, "refusal reason", forge.ReasonOf(err), tc.reason)
		})
	}
}

// The same rules hold off the lookup path, for a publication.
func TestGitHubRefusalAcceptancePublishSaysWhichRefusal(t *testing.T) {
	for status, reason := range map[int]forge.Reason{401: forge.ReasonTokenRejected, 403: forge.ReasonRepository} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				ghJSON(w, map[string]string{"message": "refused"})
			})
			_, err := f.Publish(context.Background(), ghSubmission(forge.EventApprove))
			ghClass(t, err, forge.ClassForbidden)
			ghEqual(t, "refusal reason", forge.ReasonOf(err), reason)
		})
	}
}

// An adapter with no token source refuses as no_token before asking GitHub.
func TestGitHubRefusalAcceptanceNoTokenConfiguredNeverCallsGitHub(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	f := gh.New(gh.Config{BaseURL: server.URL, Host: "github.com", HTTP: server.Client()})
	_, err := f.Resolve(context.Background(), fixtureTarget)
	ghClass(t, err, forge.ClassForbidden)
	ghEqual(t, "refusal reason", forge.ReasonOf(err), forge.ReasonNoToken)
	ghEqual(t, "no request without a token", calls.Load(), int32(0))
}

// The production token source, with no GH_TOKEN, no GITHUB_TOKEN and no gh
// to ask, refuses as no_token; it fails before any request leaves.
func TestGitHubRefusalAcceptanceProductionWithoutAnyTokenIsNoToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", t.TempDir())
	cfg := gh.Production()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	cfg.BaseURL, cfg.HTTP = server.URL, server.Client()
	_, err := gh.New(cfg).Resolve(context.Background(), fixtureTarget)
	ghClass(t, err, forge.ClassForbidden)
	ghEqual(t, "refusal reason", forge.ReasonOf(err), forge.ReasonNoToken)
	ghEqual(t, "no request without a token", calls.Load(), int32(0))
}
