package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartCapacityAcceptanceRefusesBeforeWritingConfigOrExecuting(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			layout := startTestLayout(t)
			if err := os.WriteFile(layout.BaseConfig, []byte(startConfigFixture), 0600); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			rt := startRuntime{Layout: layout, Stdout: &bytes.Buffer{}, Stderr: &stderr,
				Capacity: func() (homeSpace, error) {
					return homeSpace{Path: t.TempDir(), Free: capacityRefuse - 1, Total: 2 << 30}, nil
				},
				Exec: func(string, []string, []string) error {
					t.Fatal("agent executed with too little home capacity")
					return nil
				}}
			if code := runStart([]string{agent}, rt); code != 1 {
				t.Fatalf("start exit = %d, want 1", code)
			}
			assertCapacityText(t, stderr.String(), "agent home is full", "agent_home_gib", "Keep ~/.codex")
			if _, err := os.Stat(layout.ConfigDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("config directory created before capacity refusal: %v", err)
			}
		})
	}
}

func TestStartCapacityAcceptanceWarnsThenLaunches(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			layout := startTestLayout(t)
			if err := os.WriteFile(layout.BaseConfig, []byte(startConfigFixture), 0600); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			called := false
			rt := startRuntime{Layout: layout, Stdout: &bytes.Buffer{}, Stderr: &stderr,
				Capacity: func() (homeSpace, error) {
					return homeSpace{Path: t.TempDir(), Free: capacityRefuse, Total: 2 << 30}, nil
				},
				Exec: func(string, []string, []string) error { called = true; return nil }}
			if code := runStart([]string{agent}, rt); code != 0 || !called {
				t.Fatalf("start exit = %d, executed = %v; want launch", code, called)
			}
			assertCapacityText(t, stderr.String(), "nearly full", "agent_home_gib")
		})
	}
}

func TestStartCapacityAcceptanceHealthyOrUnreadableHomeDoesNotInterruptLaunch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		probe capacityFunc
	}{
		{"healthy", func() (homeSpace, error) { return homeSpace{Free: capacityWarn, Total: 2 << 30}, nil }},
		{"probe fails", func() (homeSpace, error) { return homeSpace{}, errors.New("statfs unavailable") }},
		{"no probe", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			called := false
			rt := startRuntime{Layout: startTestLayout(t), Stdout: &bytes.Buffer{}, Stderr: &stderr,
				Capacity: tc.probe, Exec: func(string, []string, []string) error { called = true; return nil }}
			if code := runStart([]string{"codex"}, rt); code != 0 || !called {
				t.Fatalf("start exit = %d, executed = %v; want launch", code, called)
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected capacity warning: %s", stderr.String())
			}
		})
	}
}

func TestStartCapacityAcceptanceDryRunReportsFullHomeWithoutLaunching(t *testing.T) {
	var stdout, stderr bytes.Buffer
	rt := startRuntime{Layout: startTestLayout(t), Stdout: &stdout, Stderr: &stderr,
		Capacity: func() (homeSpace, error) { return homeSpace{Path: t.TempDir(), Free: 0, Total: 2 << 30}, nil },
		Exec:     func(string, []string, []string) error { t.Fatal("dry run executed"); return nil }}
	if code := runStart([]string{"codex", "--dry-run"}, rt); code != 1 {
		t.Fatalf("dry run exit = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("dry run printed a launch plan despite refusal: %s", stdout.String())
	}
	assertCapacityText(t, stderr.String(), "0 MiB free", "agent home is full")
}

func TestStartCapacityAcceptanceRecoveryNamesDisposableCachesAndKeepsState(t *testing.T) {
	home := t.TempDir()
	cache := capacityFile{filepath.Join(home, ".cache", "go-build", "artifact"), "disposable"}
	writeCapacityFile(t, cache)
	keep := capacityFile{filepath.Join(home, ".codex", "session"), "keep me"}
	writeCapacityFile(t, keep)
	var stderr bytes.Buffer
	full := reportCapacity(func() (homeSpace, error) {
		return homeSpace{Path: home, Free: 0, Total: 2 << 30}, nil
	}, &stderr, "pairtask start")
	if !full {
		t.Fatal("full home was not reported as a start refusal")
	}
	assertCapacityText(t, stderr.String(), "~/.cache/go-build", "go clean -cache", "Keep ~/.codex", "~/.claude", "~/.pi", "~/.local/state", "agent_home_gib")
	if strings.Contains(stderr.String(), "npm cache clean") {
		t.Fatal("recovery named an absent npm cache")
	}
	assertCapacityFile(t, cache)
	assertCapacityFile(t, keep)
}

type capacityFile struct {
	path, contents string
}

func writeCapacityFile(t *testing.T, file capacityFile) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file.path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file.path, []byte(file.contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertCapacityFile(t *testing.T, file capacityFile) {
	t.Helper()
	data, err := os.ReadFile(file.path)
	if err != nil || string(data) != file.contents {
		t.Fatalf("capacity report changed %s: %q (%v)", file.path, data, err)
	}
}

func TestHomeCheckAcceptanceDoesNotFailTheShell(t *testing.T) {
	var stderr bytes.Buffer
	if code := runHomeCheck(&stderr); code != 0 {
		t.Fatalf("home-check exit = %d, want 0", code)
	}
}

func assertCapacityText(t *testing.T, message string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(message, part) {
			t.Fatalf("capacity report %q missing %q", message, part)
		}
	}
}
