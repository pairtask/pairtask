package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/resume"
)

type resumeStartFixture struct {
	runtime     startRuntime
	out, stderr bytes.Buffer
	argv        []string
	changedDir  string
	binding     resume.Binding
}

func newResumeStartFixture(t *testing.T, saved bool) *resumeStartFixture {
	t.Helper()
	f := &resumeStartFixture{}
	layout := startTestLayout(t)
	layout.StateDir, layout.CodexHome = t.TempDir(), t.TempDir()
	f.binding = resume.Binding{AgentID: "reviewer / 2", ThreadID: "saved-thread", Cwd: t.TempDir(), UpdatedAt: time.Now().UTC()}
	if saved {
		if err := (resume.Store{Dir: layout.StateDir}).Save(f.binding); err != nil {
			t.Fatal(err)
		}
	}
	f.runtime = startRuntime{Stdout: &f.out, Stderr: &f.stderr, Layout: layout,
		Status: func(context.Context, string) (protocol.StatusSnapshot, error) { return readyStatus(), nil },
		Chdir:  func(path string) error { f.changedDir = path; return nil },
		Exec:   func(_ string, argv, _ []string) error { f.argv = append([]string(nil), argv...); return nil },
	}
	return f
}

func (f *resumeStartFixture) run(extra ...string) int {
	args := []string{"codex", "--id", f.binding.AgentID}
	return runStart(append(args, extra...), f.runtime)
}

func TestStartResumeAcceptanceUsesAgentBindingForManagedSelectors(t *testing.T) {
	for _, args := range [][]string{{"--resume"}, {"--", "resume"}, {"--", "resume", "--last"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := newResumeStartFixture(t, true)
			code := f.run(args...)
			assertStartEqual(t, "resume status: "+f.stderr.String(), code, 0)
			assertStartEqual(t, "actual saved cwd selected", f.changedDir, f.binding.Cwd)
			if len(f.argv) < 2 || !slices.Equal(f.argv[len(f.argv)-2:], []string{"resume", f.binding.ThreadID}) {
				t.Fatalf("did not resume recorded thread: %q", f.argv)
			}
			for _, want := range []string{f.binding.AgentID, f.binding.ThreadID, f.binding.Cwd} {
				if !strings.Contains(f.out.String()+f.stderr.String(), want) {
					t.Errorf("resolved plan omitted %q", want)
				}
			}
		})
	}
}

func TestStartResumeAcceptanceDryRunDoesNotChangeDirectoryOrExec(t *testing.T) {
	f := newResumeStartFixture(t, true)
	assertStartEqual(t, "dry run status: "+f.stderr.String(), f.run("--resume", "--dry-run"), 0)
	assertStartEqual(t, "dry run does not change cwd", f.changedDir, "")
	assertStartEqual(t, "dry run does not execute", len(f.argv), 0)
	for _, want := range []string{f.binding.Cwd, f.binding.ThreadID} {
		if !strings.Contains(f.out.String(), want) {
			t.Errorf("dry run omitted %q", want)
		}
	}
}

func TestStartResumeAcceptanceMissingCwdWarnsAndKeepsCurrentDirectory(t *testing.T) {
	f := newResumeStartFixture(t, true)
	if err := os.Remove(f.binding.Cwd); err != nil {
		t.Fatal(err)
	}
	assertStartEqual(t, "missing cwd status", f.run("--resume"), 0)
	assertStartEqual(t, "missing cwd not selected", f.changedDir, "")
	if len(f.argv) == 0 || !strings.Contains(f.stderr.String(), f.binding.Cwd) {
		t.Fatalf("missing directory did not warn and continue: %s", f.stderr.String())
	}
}

func TestStartResumeAcceptanceRefusesConnectedEvenWithDegradedStatus(t *testing.T) {
	f := newResumeStartFixture(t, true)
	f.runtime.Status = func(context.Context, string) (protocol.StatusSnapshot, error) {
		snap := readyStatus()
		snap.Health = "degraded"
		snap.Sessions = []protocol.SessionStatus{{AgentID: f.binding.AgentID, SessionID: "live-session", State: "transport_connected"}}
		return snap, errors.New("claims unavailable")
	}
	assertStartEqual(t, "live owner refusal", f.run("--resume"), 2)
	assertStartEqual(t, "live owner not replaced", len(f.argv), 0)
	assertStartEqual(t, "refusal leaves cwd", f.changedDir, "")
	if !strings.Contains(f.stderr.String(), "still connected") || !strings.Contains(f.stderr.String(), "live-session") {
		t.Fatalf("missing live owner diagnostic: %s", f.stderr.String())
	}
}

func TestStartResumeAcceptanceUnreachableBrokerWarnsWithoutGuessingThread(t *testing.T) {
	f := newResumeStartFixture(t, true)
	f.runtime.Status = func(context.Context, string) (protocol.StatusSnapshot, error) {
		return protocol.UnavailableStatus(), errors.New("offline")
	}
	assertStartEqual(t, "unreachable preflight", f.run("--resume"), 0)
	if len(f.argv) == 0 || f.stderr.Len() == 0 {
		t.Fatal("unreachable broker must warn and leave final ownership to the launcher")
	}
}

func TestStartResumeAcceptanceMissingBindingNeverSelectsCandidateAutomatically(t *testing.T) {
	f := newResumeStartFixture(t, false)
	assertStartEqual(t, "missing binding", f.run("--resume"), 2)
	assertStartEqual(t, "missing binding never launches", len(f.argv), 0)
	if !strings.Contains(f.stderr.String()+f.out.String(), "no recorded Codex conversation") {
		t.Fatalf("missing binding diagnostic absent: %s", f.stderr.String())
	}
}

func TestStartResumeAcceptanceCorruptBindingRefusesWithoutExec(t *testing.T) {
	f := newResumeStartFixture(t, true)
	files, err := filepath.Glob(filepath.Join(f.runtime.Layout.StateDir, "codex-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("binding fixture: %v %v", files, err)
	}
	if err := os.WriteFile(files[0], []byte("{invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	assertStartEqual(t, "corrupt binding", f.run("--resume"), 2)
	assertStartEqual(t, "corrupt binding never launches", len(f.argv), 0)
	if f.stderr.Len() == 0 {
		t.Fatal("corrupt binding had no diagnostic")
	}
}

func TestStartResumeAcceptanceExplicitNativeSelectorsRemainUnchanged(t *testing.T) {
	for _, native := range [][]string{{"resume", "session name", "--model", "selected-model"}, {"resume", "--last", "--all"}, {"resume", "--cd", "/explicit", "thread-id"}, {"fork", "parent-id"}} {
		t.Run(strings.Join(native, " "), func(t *testing.T) {
			f := newResumeStartFixture(t, false)
			assertStartEqual(t, "explicit native status: "+f.stderr.String(), f.run(append([]string{"--"}, native...)...), 0)
			if len(f.argv) < len(native) || !slices.Equal(f.argv[len(f.argv)-len(native):], native) {
				t.Fatalf("native arguments rewritten: %q", f.argv)
			}
			assertStartEqual(t, "native args own cwd selection", f.changedDir, "")
		})
	}
}

func TestStartResumeAcceptanceBrokerSessionIDIsNotAConversationID(t *testing.T) {
	f := newResumeStartFixture(t, false)
	f.runtime.Status = func(context.Context, string) (protocol.StatusSnapshot, error) {
		snap := readyStatus()
		snap.Sessions = []protocol.SessionStatus{{AgentID: "old-agent", SessionID: "broker-session", State: "disconnected"}}
		return snap, nil
	}
	assertStartEqual(t, "broker ID rejected", f.run("--", "resume", "broker-session"), 2)
	assertStartEqual(t, "broker ID never launched", len(f.argv), 0)
	if !strings.Contains(f.stderr.String(), "pairtask session ID, not a Codex conversation ID") {
		t.Fatalf("missing ID distinction: %s", f.stderr.String())
	}
}

func TestStartResumeAcceptanceConflictingResumeOptionsAreUsageErrors(t *testing.T) {
	for _, native := range [][]string{{"resume", "explicit"}, {"fork", "parent"}} {
		f := newResumeStartFixture(t, true)
		assertStartEqual(t, "conflicting resume", f.run(append([]string{"--resume", "--"}, native...)...), 2)
		assertStartEqual(t, "conflict never launches", len(f.argv), 0)
	}
}

func TestStartResumeAcceptanceBrokerIDInNativeValuesIsForwarded(t *testing.T) {
	for _, native := range [][]string{
		{"resume", "valid-thread", "broker-session"},
		{"resume", "valid-thread", "--model", "broker-session"},
		{"resume", "--cd", "broker-session", "valid-thread"},
	} {
		t.Run(strings.Join(native, " "), func(t *testing.T) {
			f := newResumeStartFixture(t, false)
			f.runtime.Status = func(context.Context, string) (protocol.StatusSnapshot, error) {
				snap := readyStatus()
				snap.Sessions = []protocol.SessionStatus{{AgentID: "other", SessionID: "broker-session", State: "disconnected"}}
				return snap, nil
			}
			assertStartEqual(t, "native values are not selectors: "+f.stderr.String(), f.run(append([]string{"--"}, native...)...), 0)
			if len(f.argv) < len(native) || !slices.Equal(f.argv[len(f.argv)-len(native):], native) {
				t.Fatalf("native arguments rewritten: %q", f.argv)
			}
		})
	}
}

func TestStartResumeAcceptanceLifecycleWordsInOptionValuesAreForwarded(t *testing.T) {
	for _, model := range []string{"fork", "resume"} {
		t.Run(model, func(t *testing.T) {
			f := newResumeStartFixture(t, true)
			assertStartEqual(t, "model value is not a lifecycle command", f.run("--resume", "--", "--model", model), 0)
			want := []string{"--model", model, "resume", f.binding.ThreadID}
			if len(f.argv) < len(want) || !slices.Equal(f.argv[len(f.argv)-len(want):], want) {
				t.Fatalf("managed resume lost native options: %q", f.argv)
			}
		})
	}
}
