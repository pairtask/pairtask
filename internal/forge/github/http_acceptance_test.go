package github_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pairtask/pairtask/internal/forge"
	gh "github.com/pairtask/pairtask/internal/forge/github"
	"github.com/pairtask/pairtask/internal/protocol"
)

const fixtureToken = "fixture-private-token"

var fixturePins = forge.Revisions{Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40)}
var fixtureTarget = forge.Target{Forge: "github", Repository: "github.com/owner/repo", Change: "42"}

func ghEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v; want %v", label, got, want)
	}
}
func ghOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func ghClass(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected forge error")
	}
	ghEqual(t, "error class", forge.ClassOf(err), want)
	if strings.Contains(err.Error(), fixtureToken) {
		t.Fatal("credential leaked in error")
	}
}
func ghJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func ghFixture(t *testing.T, handler http.HandlerFunc) forge.Forge {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fixtureToken {
			t.Error("missing fixture authorization")
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("missing GitHub media type")
		}
		if r.Header.Get("X-GitHub-Api-Version") == "" {
			t.Error("missing pinned API version")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return gh.New(gh.Config{BaseURL: server.URL, Host: "github.com", Token: func(context.Context) (string, error) { return fixtureToken, nil }, HTTP: server.Client(), MaxPages: 2})
}

func ghReceipt(state, body string) map[string]any {
	return map[string]any{"id": 123, "html_url": "https://github.com/owner/repo/pull/42#pullrequestreview-123", "user": map[string]string{"login": "review-account"}, "commit_id": fixturePins.Head, "state": state, "body": body}
}
func ghSubmission(event string) forge.Submission {
	return forge.Submission{Target: fixtureTarget, Pinned: fixturePins, Event: event, Summary: "review summary", Identity: "review-account", Marker: forge.Marker("request-id", 1, 1), Findings: []protocol.Finding{{Path: "src/a.go", Line: 12, Summary: "line finding"}, {Path: "binary.png", Summary: "file finding"}}}
}
func ghExpected() forge.Expected {
	s := ghSubmission(forge.EventApprove)
	return forge.Expected{Marker: s.Marker, Identity: s.Identity, Revision: s.Pinned.Head, Event: s.Event}
}

func TestGitHubAcceptanceIdentityAndImmutablePins(t *testing.T) {
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
		ghEqual(t, "read method", r.Method, "GET")
		switch r.URL.Path {
		case "/user":
			ghJSON(w, map[string]string{"login": "review-account"})
		case "/repos/owner/repo/pulls/42":
			ghJSON(w, map[string]any{"base": map[string]string{"sha": fixturePins.Base}, "head": map[string]string{"sha": fixturePins.Head}, "state": "closed", "merged": true})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	identity, err := f.Identity(context.Background())
	ghOK(t, err)
	ghEqual(t, "identity", identity, "review-account")
	pins, err := f.Resolve(context.Background(), fixtureTarget)
	ghOK(t, err)
	ghEqual(t, "both full pins", pins, fixturePins)
}

func TestGitHubAcceptancePublishExactEventHeadAndFindings(t *testing.T) {
	for _, tc := range []struct{ event, wire, state string }{{forge.EventApprove, "APPROVE", "APPROVED"}, {forge.EventRequestChanges, "REQUEST_CHANGES", "CHANGES_REQUESTED"}, {forge.EventComment, "COMMENT", "COMMENTED"}} {
		t.Run(tc.event, func(t *testing.T) {
			sub := ghSubmission(tc.event)
			f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
				ghEqual(t, "method", r.Method, "POST")
				ghEqual(t, "path", r.URL.Path, "/repos/owner/repo/pulls/42/reviews")
				var body struct {
					Commit      string `json:"commit_id"`
					Event, Body string
					Comments    []struct {
						Path       string
						Line       int
						Side, Body string
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				ghEqual(t, "exact head", body.Commit, fixturePins.Head)
				ghEqual(t, "actual event", body.Event, tc.wire)
				for _, want := range []string{sub.Summary, "src/a.go:12", "binary.png", "line finding", "file finding", sub.Marker} {
					if !strings.Contains(body.Body, want) {
						t.Errorf("body missing %q", want)
					}
				}
				ghEqual(t, "one inline finding", len(body.Comments), 1)
				if len(body.Comments) == 1 {
					c := body.Comments[0]
					ghEqual(t, "inline path", c.Path, "src/a.go")
					ghEqual(t, "new-side line", c.Line, 12)
					ghEqual(t, "side", c.Side, "RIGHT")
					ghEqual(t, "inline body", c.Body, "line finding")
				}
				ghJSON(w, ghReceipt(tc.state, body.Body))
			})
			receipt, err := f.Publish(context.Background(), sub)
			ghOK(t, err)
			ghEqual(t, "provider ID", receipt.ReviewID, "123")
			ghEqual(t, "event", receipt.Event, tc.event)
			ghEqual(t, "formal", receipt.Formal, tc.event != forge.EventComment)
			ghEqual(t, "account", receipt.Identity, sub.Identity)
			ghEqual(t, "recorded head", receipt.Revision, sub.Pinned.Head)
		})
	}
}

func TestGitHubAcceptanceForeignRepositoryNeverUsesCredentials(t *testing.T) {
	var calls atomic.Int32
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })
	target := fixtureTarget
	target.Repository = "other.example/owner/repo"
	_, err := f.Resolve(context.Background(), target)
	ghClass(t, err, forge.ClassNotFound)
	ghEqual(t, "no network call", calls.Load(), 0)
}

func TestGitHubAcceptanceRejectsMalformedResolvedPins(t *testing.T) {
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
		ghJSON(w, map[string]any{"base": map[string]string{"sha": "main"}, "head": map[string]string{"sha": fixturePins.Head}})
	})
	_, err := f.Resolve(context.Background(), fixtureTarget)
	ghClass(t, err, forge.ClassInvalid)
}
