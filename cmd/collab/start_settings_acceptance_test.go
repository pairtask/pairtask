package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func startSettingsFixture(t *testing.T) startLayout {
	t.Helper()
	layout := startTestLayout(t)
	layout.ClaudeSettings = filepath.Join(t.TempDir(), "installed permissions with spaces.json")
	if err := os.WriteFile(layout.ClaudeSettings, []byte(`{"permissions":{"allow":["mcp__pairtask__send"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.BaseConfig, []byte(startConfigFixture), 0600); err != nil {
		t.Fatal(err)
	}
	return layout
}

func TestStartSettingsInstalledDefault(t *testing.T) {
	assertStartEqual(t, "installed Claude permissions", defaultStartLayout().ClaudeSettings, "/etc/pairtask/claude-settings.json")
}

func TestStartSettingsFreshAndResumedLaunch(t *testing.T) {
	for _, native := range [][]string{nil, {"--resume", "saved session", "--model", "native model"}, {"--settings", "explicit user settings.json", "literal prompt ; $(ignored)"}} {
		t.Run(strings.Join(native, " "), func(t *testing.T) {
			layout := startSettingsFixture(t)
			args := append([]string{"claude", "--model", "opus[1m]", "--effort", "high", "--"}, native...)
			want := []string{layout.ClaudeBinary, "--strict-mcp-config", "--mcp-config", layout.configPath("claude-1"), "--dangerously-load-development-channels", "server:pairtask", "--model", "opus[1m]", "--effort", "high", "--settings", layout.ClaudeSettings}
			want = append(want, native...)
			var out, stderr bytes.Buffer
			calls := 0
			code := runStart(args, startRuntime{Layout: layout, Stdout: &out, Stderr: &stderr, Exec: func(path string, argv, env []string) error {
				calls++
				assertStartEqual(t, "executable", path, layout.ClaudeBinary)
				assertStartEqual(t, "launch arguments including permissions before native args", argv, want)
				if _, err := os.Stat(layout.configPath("claude-1")); err != nil {
					t.Fatal(err)
				}
				return errors.New("settings launch observed")
			}})
			assertStartEqual(t, "native launcher called", calls, 1)
			assertStartEqual(t, "launcher result", code, 1)
		})
	}
}

func TestStartSettingsAbsentPreservesNativeHost(t *testing.T) {
	layout := startTestLayout(t)
	layout.ClaudeSettings = filepath.Join(t.TempDir(), "not installed.json")
	opts := parseStartForTest(t, "claude", "--", "--resume", "saved session")
	absent := buildStartForTest(t, opts, layout)
	layout.ClaudeSettings = ""
	unconfigured := buildStartForTest(t, opts, layout)
	assertStartEqual(t, "missing installed permissions preserves native host argv", absent.Argv, unconfigured.Argv)
	if slices.Contains(absent.Argv, "--settings") {
		t.Fatal("missing settings file passed to Claude")
	}
}

func TestStartSettingsInvalidInstallationFailsBeforeLaunch(t *testing.T) {
	for _, kind := range []string{"directory", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			layout := startSettingsFixture(t)
			if kind == "directory" {
				if err := os.Remove(layout.ClaudeSettings); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(layout.ClaudeSettings, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Chmod(layout.ClaudeSettings, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(layout.ClaudeSettings, 0600) })
				if _, err := os.ReadFile(layout.ClaudeSettings); err == nil {
					t.Skip("privileged runner can read mode-000 files")
				}
			}
			var out, stderr bytes.Buffer
			calls := 0
			code := runStart([]string{"claude"}, startRuntime{Layout: layout, Stdout: &out, Stderr: &stderr, Exec: func(string, []string, []string) error { calls++; return nil }})
			if code == 0 || calls != 0 {
				t.Errorf("invalid permissions installation launched: code=%d calls=%d", code, calls)
			}
			if !strings.Contains(stderr.String(), layout.ClaudeSettings) {
				t.Errorf("diagnostic must identify permissions file: %s", &stderr)
			}
			if _, err := os.Stat(layout.ConfigDir); !os.IsNotExist(err) {
				t.Errorf("invalid installation wrote generated config: %v", err)
			}
		})
	}
}

func TestStartSettingsDoNotAffectOtherHarnesses(t *testing.T) {
	for _, agent := range []string{"codex", "pi"} {
		t.Run(agent, func(t *testing.T) {
			layout := startSettingsFixture(t)
			layout.ClaudeSettings = t.TempDir() // invalid for Claude; other agents must not inspect it.
			opts := parseStartForTest(t, agent)
			got := buildStartForTest(t, opts, layout)
			layout.ClaudeSettings = ""
			want := buildStartForTest(t, opts, layout)
			assertStartEqual(t, "other harness plan", got, want)
		})
	}
}
