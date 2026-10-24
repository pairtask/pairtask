package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

const startConfigFixture = `{
  "metadata": {"keep": true},
  "mcpServers": {
    "pairtask": {
      "command": "/installed/pairtask-mcp",
      "args": ["--agent-id", "claude-1", "--socket", "/old.sock", "--claude-channel", "--auto-listen"],
      "env": {"KEEP": "configured value"}
    },
    "other": {"command": "/installed/other", "args": ["a path"], "env": {"TOKEN": "test-secret"}}
  }
}`

func startTestLayout(t *testing.T) startLayout {
	t.Helper()
	dir := t.TempDir()
	return startLayout{
		CodexLauncher: "/installed/pairtask-codex", CodexBinary: "/installed/codex",
		ClaudeBinary: "/installed/claude", BaseConfig: filepath.Join(dir, "base.json"),
		ConfigDir: filepath.Join(dir, "private configs"),
	}
}

func parseStartForTest(t *testing.T, args ...string) startOptions {
	t.Helper()
	opts, err := parseStart(args)
	if err != nil {
		t.Fatalf("parseStart(%q): %v", args, err)
	}
	return opts
}

func buildStartForTest(t *testing.T, opts startOptions, layout startLayout) startPlan {
	t.Helper()
	plan, err := buildStart(opts, layout, []byte(startConfigFixture))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func decodeStartConfig(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func assertStartMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), mode)
	}
}

func TestStartDefaultsAndNativeArgumentBoundary(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			opts := parseStartForTest(t, agent)
			if opts.Agent != agent || opts.ID != agent+"-1" {
				t.Fatalf("defaults: %+v", opts)
			}
			native := []string{"resume", "session with spaces", "--model", "native-model", "--dry-run"}
			opts = parseStartForTest(t, append([]string{agent, "--id", "reviewer / 2", "--"}, native...)...)
			if opts.ID != "reviewer / 2" || opts.DryRun || !reflect.DeepEqual(opts.Native, native) {
				t.Fatalf("native arguments consumed or changed: %+v", opts)
			}
		})
	}
}

func TestStartCommandIsRoutedByPublicCLI(t *testing.T) {
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"start", "--help"}, &out, &stderr); code != 0 {
		t.Fatalf("public start help returned %d: %s", code, &stderr)
	}
	if !strings.Contains(out.String(), "pairtask start") {
		t.Fatalf("missing startup help: %s", &out)
	}
}

func TestStartRejectsInvalidIdentityAndOptions(t *testing.T) {
	cases := [][]string{
		{}, {"unknown"}, {"codex", "--id", ""}, {"claude", "--id", "operator"},
		{"codex", "--id", "*"}, {"claude", "--id", strings.Repeat("é", 65)},
		{"codex", "--id", "nul\x00id"}, {"claude", "--model", "nul\x00model"},
		{"codex", "--effort", "high\nother=value"}, {"claude", "--unknown"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := parseStart(args); err == nil {
				t.Fatalf("accepted invalid options %q", args)
			}
		})
	}
	if _, err := parseStart([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
}

func TestStartCodexReusesManagedLauncherWithLiteralArguments(t *testing.T) {
	layout := startTestLayout(t)
	opts := parseStartForTest(t, "codex", "--id", "review / 2", "--socket", "/tmp/a socket",
		"--model", "provider/model:v1", "--effort", "high", "--", "resume", "saved session", "-C", "/workspace/a repo")
	plan := buildStartForTest(t, opts, layout)
	want := []string{layout.CodexLauncher, "--codex", layout.CodexBinary, "--agent-id", "review / 2",
		"--socket", "/tmp/a socket", "--terminal", "--", "--model", "provider/model:v1",
		"-c", `model_reasoning_effort="high"`, "resume", "saved session", "-C", "/workspace/a repo"}
	if !reflect.DeepEqual(plan.Argv, want) {
		t.Fatalf("argv = %q, want %q", plan.Argv, want)
	}
	if len(plan.Config) != 0 || plan.ConfigPath != "" {
		t.Fatal("Codex must not create Claude configuration")
	}
	defaults := buildStartForTest(t, parseStartForTest(t, "codex"), layout)
	wantDefaults := []string{layout.CodexLauncher, "--codex", layout.CodexBinary, "--agent-id", "codex-1",
		"--socket", "/tmp/collab-ai.sock", "--terminal", "--"}
	if !reflect.DeepEqual(defaults.Argv, wantDefaults) {
		t.Fatalf("default argv = %q", defaults.Argv)
	}
}

func TestStartClaudePreservesConfigurationAndForwardsModelEffort(t *testing.T) {
	layout := startTestLayout(t)
	opts := parseStartForTest(t, "claude", "--id", "reviewer-2", "--socket", "/new socket",
		"--model", "opus[1m]", "--effort", "high", "--", "--resume", "saved session")
	plan := buildStartForTest(t, opts, layout)
	want := []string{layout.ClaudeBinary, "--strict-mcp-config", "--mcp-config", plan.ConfigPath,
		"--dangerously-load-development-channels", "server:pairtask", "--model", "opus[1m]",
		"--effort", "high", "--resume", "saved session"}
	if !reflect.DeepEqual(plan.Argv, want) {
		t.Fatalf("argv = %q, want %q", plan.Argv, want)
	}
	expected := decodeStartConfig(t, []byte(startConfigFixture))
	server := expected["mcpServers"].(map[string]any)["pairtask"].(map[string]any)
	server["args"] = []any{"--agent-id", "reviewer-2", "--socket", "/new socket", "--claude-channel", "--auto-listen"}
	if actual := decodeStartConfig(t, plan.Config); !reflect.DeepEqual(actual, expected) {
		t.Fatalf("unrelated configuration changed: %#v", actual)
	}
}

func TestStartClaudeConfigSupportsEqualsFlagsAndMissingSocket(t *testing.T) {
	cases := []struct {
		original, socket string
		want             []string
	}{
		{`["--agent-id=old", "--socket=/old", "--auto-listen"]`, "/new", []string{"--agent-id=new", "--socket=/new", "--auto-listen"}},
		{`["--agent-id", "old", "--auto-listen"]`, "/new", []string{"--agent-id", "new", "--auto-listen", "--socket", "/new"}},
		{`["--agent-id", "old", "--socket", "/keep"]`, "", []string{"--agent-id", "new", "--socket", "/keep"}},
	}
	for _, tc := range cases {
		t.Run(tc.original, func(t *testing.T) {
			base := []byte(`{"mcpServers":{"pairtask":{"args":` + tc.original + `}}}`)
			got, err := claudeConfig(base, "new", tc.socket)
			if err != nil {
				t.Fatal(err)
			}
			config := decodeStartConfig(t, got)
			args := config["mcpServers"].(map[string]any)["pairtask"].(map[string]any)["args"]
			wantJSON, _ := json.Marshal(tc.want)
			gotJSON, _ := json.Marshal(args)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("args = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestStartClaudeRejectsMalformedOrAmbiguousConfig(t *testing.T) {
	cases := []string{`null`, `{}`, `{"mcpServers":[]}`, `{"mcpServers":{"pairtask":null}}`,
		`{"mcpServers":{"pairtask":{"args":[7]}}}`,
		`{"mcpServers":{"pairtask":{"args":["--auto-listen"]}}}`,
		`{"mcpServers":{"pairtask":{"args":["--agent-id"]}}}`,
		`{"mcpServers":{"pairtask":{"args":["--agent-id","--socket","/old"]}}}`,
		`{"mcpServers":{"pairtask":{"args":["--agent-id","one","--agent-id=two"]}}}`,
		`{"mcpServers":{"pairtask":{"args":["--agent-id","one","--agent-id"]}}}`,
		`{"mcpServers":{"pairtask":{"args":["--agent-id","one","--socket"]}}}`,
		`{"mcpServers":{"pairtask":{"args":["--agent-id","one","--socket","a","--socket=b"]}}}`}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			if _, err := claudeConfig([]byte(raw), "reviewer", "/new"); err == nil {
				t.Fatalf("accepted malformed config: %s", raw)
			}
		})
	}
}

func TestStartConfigPathsContainArbitraryValidIDs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "configs")
	seen := map[string]bool{}
	for _, id := range []string{"reviewer-2", "../../outside", "team/../two", "team two", "评审"} {
		path := configPath(dir, id)
		if filepath.Dir(path) != dir || filepath.Ext(path) != ".json" {
			t.Fatalf("ID %q escaped config directory: %s", id, path)
		}
		if seen[path] || configPath(dir, id) != path {
			t.Fatalf("unstable or colliding path for %q", id)
		}
		seen[path] = true
	}
}

func TestStartDryRunAndInvalidInputWriteNothing(t *testing.T) {
	layout := startTestLayout(t)
	if err := os.WriteFile(layout.BaseConfig, []byte(startConfigFixture), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	executed := false
	exec := func(string, []string, []string) error { executed = true; return nil }
	code := runStart([]string{"claude", "--id", "reviewer", "--dry-run"}, &out, &stderr, layout, exec)
	if code != 0 || executed {
		t.Fatalf("dry run: code=%d executed=%v stderr=%s", code, executed, &stderr)
	}
	if strings.Contains(out.String(), "test-secret") {
		t.Fatal("dry run disclosed other server's secret")
	}
	if _, err := os.Stat(layout.ConfigDir); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote config directory: %v", err)
	}
	code = runStart([]string{"claude", "--id", "operator"}, &out, &stderr, layout, exec)
	if code != 2 || executed {
		t.Fatalf("invalid ID: code=%d executed=%v", code, executed)
	}
	if _, err := os.Stat(layout.ConfigDir); !os.IsNotExist(err) {
		t.Fatalf("invalid ID wrote config directory: %v", err)
	}
}

func TestStartExecReceivesPrivateConfigAndUnchangedEnvironment(t *testing.T) {
	layout := startTestLayout(t)
	if err := os.WriteFile(layout.BaseConfig, []byte(startConfigFixture), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PAIRTASK_START_TEST", "literal value")
	wantEnv := os.Environ()
	var out, stderr bytes.Buffer
	calls := 0
	exec := func(path string, argv, env []string) error {
		calls++
		if path != layout.ClaudeBinary || argv[0] != path {
			t.Fatalf("wrong executable: %s %q", path, argv)
		}
		if !reflect.DeepEqual(env, wantEnv) {
			t.Fatal("launch environment changed")
		}
		config := configPath(layout.ConfigDir, "reviewer-2")
		assertStartMode(t, config, 0600)
		assertStartMode(t, layout.ConfigDir, 0700)
		return errors.New("test exec failure")
	}
	code := runStart([]string{"claude", "--id", "reviewer-2"}, &out, &stderr, layout, exec)
	if code != 1 || calls != 1 || !strings.Contains(stderr.String(), "test exec failure") {
		t.Fatalf("exec failure: code=%d calls=%d stderr=%s", code, calls, &stderr)
	}
	unchanged, err := os.ReadFile(layout.BaseConfig)
	if err != nil || string(unchanged) != startConfigFixture {
		t.Fatalf("installed config modified: %v", err)
	}
}

func TestStartPrivateWriteReplacesWithoutFollowingTargetSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.json")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "managed.json")
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(path, []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(outside)
	if err != nil || string(original) != "keep" {
		t.Fatalf("followed symlink: %q %v", original, err)
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != "replacement" {
		t.Fatalf("write: %q %v", current, err)
	}
}

func TestStartPrivateWriteFailurePreservesPreviousConfig(t *testing.T) {
	if path := os.Getenv("PAIRTASK_TEST_LIMITED_WRITE"); path != "" {
		startLimitedWrite(t, path)
		return
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("previous config"), 0600); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestStartPrivateWriteFailurePreservesPreviousConfig$")
	child.Env = append(os.Environ(), "PAIRTASK_TEST_LIMITED_WRITE="+path)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("limited-write child: %v\n%s", err, output)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "previous config" {
		t.Fatalf("failed write replaced prior config: %q (%v)", got, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v (%v)", entries, err)
	}
}

func startLimitedWrite(t *testing.T, path string) {
	t.Helper()
	// Limit only the child process: a real short write must not publish a
	// truncated configuration, and the parent test runner keeps its limits.
	signal.Ignore(syscall.SIGXFSZ)
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	limit.Cur = 1
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(path, []byte("replacement config")); err == nil {
		t.Fatal("partial write was reported as successful")
	}
}

func TestStartExecPreservesNativeExitStatus(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) { startExecProof(t, agent) })
	}
}

func startExecProof(t *testing.T, agent string) {
	t.Helper()
	layout := startTestLayout(t)
	fake := filepath.Join(t.TempDir(), "native launcher")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf 'native launcher reached\\n'\nexit 23\n"), 0700); err != nil {
		t.Fatal(err)
	}
	layout.CodexLauncher, layout.ClaudeBinary = fake, fake
	if err := os.WriteFile(layout.BaseConfig, []byte(startConfigFixture), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(layout)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestStartExecChild$")
	child.Env = append(os.Environ(), "PAIRTASK_TEST_EXEC_LAYOUT="+string(data), "PAIRTASK_TEST_EXEC_AGENT="+agent)
	output, err := child.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		t.Fatalf("child did not exit through native launcher: %v\n%s", err, output)
	}
	if exited.ExitCode() != 23 || string(output) != "native launcher reached\n" {
		t.Fatalf("native status/output changed: %v\n%s", err, output)
	}
}

func TestStartExecChild(t *testing.T) {
	raw := os.Getenv("PAIRTASK_TEST_EXEC_LAYOUT")
	if raw == "" {
		t.Skip("subprocess helper")
	}
	var layout startLayout
	if err := json.Unmarshal([]byte(raw), &layout); err != nil {
		t.Fatal(err)
	}
	code := runStart([]string{os.Getenv("PAIRTASK_TEST_EXEC_AGENT"), "--id", "exec-proof"},
		os.Stdout, os.Stderr, layout, syscall.Exec)
	t.Fatalf("exec returned instead of replacing this process: %d", code)
}
