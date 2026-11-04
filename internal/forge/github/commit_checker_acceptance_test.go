package github_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/forge"
)

// Keep the baseline compilable before the optional production interface is
// introduced, so absence of the capability is a behavioral failure.
type advisoryCommitChecker interface {
	CommitExists(context.Context, forge.Target, string) (bool, error)
}

func advisoryChecker(t *testing.T, f forge.Forge) advisoryCommitChecker {
	t.Helper()
	c, ok := f.(advisoryCommitChecker)
	if !ok {
		t.Fatal("GitHub adapter cannot check an explicit pinned commit")
	}
	return c
}

func TestCommitCheckerAcceptanceDistinguishesMissingFromInaccessible(t *testing.T) {
	for _, tc := range []struct {
		status int
		exists bool
		class  string
	}{
		{200, true, ""}, {404, false, ""}, {422, false, forge.ClassInvalid},
		{403, false, forge.ClassForbidden}, {429, false, forge.ClassRateLimited}, {500, false, forge.ClassUnavailable},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			var calls atomic.Int32
			f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				ghEqual(t, "commit lookup method", r.Method, http.MethodGet)
				ghEqual(t, "lookup uses explicit immutable pin", r.URL.Path, "/repos/owner/repo/commits/"+fixturePins.Head)
				w.WriteHeader(tc.status)
				ghJSON(w, map[string]string{"sha": fixturePins.Head, "message": "fixture failure"})
			})
			exists, err := advisoryChecker(t, f).CommitExists(context.Background(), fixtureTarget, fixturePins.Head)
			if tc.class == "" {
				ghOK(t, err)
			} else {
				ghClass(t, err, tc.class)
			}
			ghEqual(t, "only definitive responses establish existence", exists, tc.exists)
			ghEqual(t, "one read-only commit lookup", calls.Load(), 1)
		})
	}
}

func TestCommitCheckerAcceptanceForeignRepositoryNeverUsesCredentials(t *testing.T) {
	var calls atomic.Int32
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })
	target := fixtureTarget
	target.Repository = "other.example/owner/repo"
	exists, err := advisoryChecker(t, f).CommitExists(context.Background(), target, fixturePins.Head)
	ghClass(t, err, forge.ClassNotFound)
	ghEqual(t, "foreign repository is not reported present", exists, false)
	ghEqual(t, "foreign host never receives credentials", calls.Load(), 0)
}

func TestCommitCheckerAcceptanceCanceledContextNeverReportsMissing(t *testing.T) {
	var calls atomic.Int32
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(404) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exists, err := advisoryChecker(t, f).CommitExists(ctx, fixtureTarget, fixturePins.Head)
	if err == nil {
		t.Fatal("canceled lookup falsely became a definitive absence")
	}
	ghEqual(t, "canceled commit lookup is not positive", exists, false)
	ghEqual(t, "pre-canceled lookup sends no request", calls.Load(), 0)
}

func TestCommitCheckerAcceptanceCheckPinsClassifiesRealHTTPCancellationAsTimeout(t *testing.T) {
	for _, phase := range []string{"resolve", "commit"} {
		t.Run(phase, func(t *testing.T) {
			started := make(chan struct{})
			var once sync.Once
			f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if phase == "commit" && r.URL.Path == "/repos/owner/repo/pulls/42" {
					ghJSON(w, map[string]any{"base": map[string]string{"sha": fixturePins.Base}, "head": map[string]string{"sha": fixturePins.Head}})
					return
				}
				once.Do(func() { close(started) })
				<-r.Context().Done()
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			go func() {
				select {
				case <-started:
					cancel() // cancel only once the actual HTTP read is in flight
				case <-ctx.Done():
				}
			}()
			checks := forge.CheckPins(ctx, f, fixtureTarget, fixturePins)
			if len(checks) == 0 {
				t.Fatal("canceled HTTP read was reported as a successful pin check")
			}
			for _, check := range checks {
				ghEqual(t, "canceled HTTP read is not definitive absence", check.Code, "not_checked")
				ghEqual(t, "request cancellation survives adapter error classification", check.Class, "timeout")
			}
		})
	}
}

func TestCommitCheckerAcceptanceUnusableEvidenceNeverBecomesMissingPin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   map[string]any
	}{
		{"empty-success", 200, map[string]any{}},
		{"invalid-sha-success", 200, map[string]any{"sha": "not-a-commit"}},
		{"different-sha-success", 200, map[string]any{"sha": "cccccccccccccccccccccccccccccccccccccccc"}},
		{"generic-validation", 422, map[string]any{"message": "Validation failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/pulls/42" {
					ghJSON(w, map[string]any{"base": map[string]string{"sha": fixturePins.Base}, "head": map[string]string{"sha": fixturePins.Head}})
					return
				}
				w.WriteHeader(tc.status)
				ghJSON(w, tc.body)
			})
			exists, err := advisoryChecker(t, f).CommitExists(context.Background(), fixtureTarget, fixturePins.Head)
			ghClass(t, err, forge.ClassInvalid)
			ghEqual(t, "unusable answer does not establish existence", exists, false)
			checks := forge.CheckPins(context.Background(), f, fixtureTarget, fixturePins)
			if len(checks) == 0 {
				t.Fatal("unusable evidence was reported as a successful check")
			}
			for _, check := range checks {
				ghEqual(t, "unusable evidence is not a definitive missing pin", check.Code, "not_checked")
				ghEqual(t, "classification survives preflight", check.Class, forge.ClassInvalid)
			}
		})
	}
}

func TestCommitCheckerAcceptanceSpecificMissingCommit422IsDefinitive(t *testing.T) {
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		ghJSON(w, map[string]string{"message": "No commit found for SHA: " + fixturePins.Head})
	})
	exists, err := advisoryChecker(t, f).CommitExists(context.Background(), fixtureTarget, fixturePins.Head)
	ghOK(t, err)
	ghEqual(t, "specific provider absence is definitive", exists, false)
}

func TestCommitCheckerAcceptanceOtherEndpoint422KeepsValidationClass(t *testing.T) {
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
		ghEqual(t, "read change rather than commit", r.URL.Path, "/repos/owner/repo/pulls/42")
		w.WriteHeader(422)
		ghJSON(w, map[string]string{"message": "No commit found for SHA: " + fixturePins.Head})
	})
	_, err := f.Resolve(context.Background(), fixtureTarget)
	ghClass(t, err, forge.ClassInvalid)
}
