package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/pairtask/pairtask/internal/forge"
)

func TestGitHubAcceptancePinnedDiffUsesActualNewSideLines(t *testing.T) {
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
		ghEqual(t, "immutable comparison path", r.URL.Path, "/repos/owner/repo/compare/"+fixturePins.Base+"..."+fixturePins.Head)
		ghEqual(t, "read diff", r.Method, "GET")
		ghJSON(w, map[string]any{"files": []any{
			map[string]any{"filename": "src/a.go", "patch": "@@ -10,3 +10,4 @@\n context\n-old\n+new\n+added\n tail\n@@ -30 +31 @@\n-old\n+new"},
			map[string]any{"filename": "binary.png"},
			map[string]any{"filename": "deleted.go", "patch": "@@ -1,2 +0,0 @@\n-one\n-two"},
		}})
	})
	changes, err := f.Changes(context.Background(), fixtureTarget, fixturePins)
	ghOK(t, err)
	want := []forge.FileChange{{Path: "src/a.go", Lines: []forge.LineRange{{From: 10, To: 13}, {From: 31, To: 31}}}, {Path: "binary.png"}, {Path: "deleted.go"}}
	// Nil and empty line slices are equivalent; line membership is what matters.
	ghEqual(t, "file count", len(changes), len(want))
	for i, w := range want {
		if i >= len(changes) {
			break
		}
		ghEqual(t, "path", changes[i].Path, w.Path)
		if len(w.Lines) == 0 {
			ghEqual(t, "no positive lines", len(changes[i].Lines), 0)
		} else if !reflect.DeepEqual(changes[i].Lines, w.Lines) {
			t.Errorf("new-side ranges = %+v, want %+v", changes[i].Lines, w.Lines)
		}
	}
}

func TestGitHubAcceptanceTruncatedPatchDoesNotAuthorizeMissingLines(t *testing.T) {
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
		ghJSON(w, map[string]any{"files": []any{map[string]any{"filename": "a.go", "patch": "@@ -1,3 +1,3 @@\n only one line"}}})
	})
	changes, err := f.Changes(context.Background(), fixtureTarget, fixturePins)
	if err != nil {
		return
	}
	for _, change := range changes {
		for _, lines := range change.Lines {
			if lines.To >= 2 {
				t.Fatalf("truncated hunk authorized absent lines: %+v", changes)
			}
		}
	}
}

func TestGitHubAcceptanceFileCapFailsClosed(t *testing.T) {
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
		files := make([]any, 300)
		for i := range files {
			files[i] = map[string]any{"filename": "binary.png"}
		}
		ghJSON(w, map[string]any{"files": files})
	})
	_, err := f.Changes(context.Background(), fixtureTarget, fixturePins)
	if err == nil {
		t.Fatal("300-file boundary must not silently claim a complete comparison")
	}
}

func TestGitHubAcceptanceRedirectCannotForwardAuthorization(t *testing.T) {
	var leaked atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Add(1)
		ghJSON(w, map[string]string{"login": "attacker"})
	}))
	defer sink.Close()
	f := ghFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+"/user", http.StatusTemporaryRedirect)
	})
	_, err := f.Identity(context.Background())
	if err == nil {
		t.Error("redirect to another API origin must be rejected")
	}
	ghEqual(t, "redirect target never contacted", leaked.Load(), 0)
}
