package main

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func piEnvValue(t *testing.T, env []string, key string) string {
	t.Helper()
	var values []string
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, key+"="); ok {
			values = append(values, value)
		}
	}
	if len(values) != 1 {
		t.Fatalf("%s occurs %d times, want exactly once", key, len(values))
	}
	return values[0]
}

func TestPiStartAcceptanceLiteralArgumentsAndAdapterEnvironment(t *testing.T) {
	t.Setenv("PAIRTASK_AGENT_ID", "stale-agent")
	t.Setenv("PAIRTASK_PI_EXTENSION", "/tmp/extension with spaces/index.ts")
	t.Setenv("PAIRTASK_ROOMS", `["stale-room"]`)
	t.Setenv("PAIRTASK_START_TEST", "preserved")
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	calls := 0
	rt := startRuntime{Stdout: &out, Stderr: &stderr, Layout: defaultStartLayout(), Exec: func(path string, argv, env []string) error {
		calls++
		if len(argv) < 3 || argv[0] != path || argv[1] != "-e" {
			t.Fatalf("Pi extension not loaded: %q", argv)
		}
		assertStartEqual(t, "extension override", argv[2], "/tmp/extension with spaces/index.ts")
		want := []string{"--model", "provider/model with spaces", "--mode", "rpc", "literal $value; $(nothing)", "--room", "native-room"}
		assertStartEqual(t, "exact native tail", argv[3:], want)
		assertStartEqual(t, "agent identity", piEnvValue(t, env, "PAIRTASK_AGENT_ID"), "pi-worker")
		assertStartEqual(t, "broker socket", piEnvValue(t, env, "PAIRTASK_SOCKET"), "/tmp/a socket")
		assertStartEqual(t, "unrelated environment", piEnvValue(t, env, "PAIRTASK_START_TEST"), "preserved")
		var rooms []string
		if err := json.Unmarshal([]byte(piEnvValue(t, env, "PAIRTASK_ROOMS")), &rooms); err != nil {
			t.Fatal(err)
		}
		assertStartEqual(t, "join rooms", rooms, []string{"alpha", "beta"})
		if piEnvValue(t, env, "PAIRTASK_MCP") == "" {
			t.Fatal("missing MCP executable")
		}
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		assertStartEqual(t, "cwd retained", cwd, before)
		return nil
	}}
	code := runStart([]string{"pi", "--id", "pi-worker", "--socket", "/tmp/a socket", "--room", "alpha", "--room", "beta", "--model", "provider/model with spaces", "--", "--mode", "rpc", "literal $value; $(nothing)", "--room", "native-room"}, rt)
	assertStartEqual(t, "Pi launch: "+stderr.String(), code, 0)
	assertStartEqual(t, "one exec", calls, 1)
	assertStartEqual(t, "parent environment unchanged", os.Getenv("PAIRTASK_AGENT_ID"), "stale-agent")
}

func TestPiStartAcceptanceDryRunNeverExecutesOrWritesConfig(t *testing.T) {
	var out, stderr bytes.Buffer
	layout := defaultStartLayout()
	layout.ConfigDir = t.TempDir() + "/not-created"
	rt := startRuntime{Stdout: &out, Stderr: &stderr, Layout: layout, Exec: func(string, []string, []string) error { t.Fatal("dry run executed"); return nil }}
	code := runStart([]string{"pi", "--id", "pi-dry", "--room", "alpha", "--dry-run"}, rt)
	assertStartEqual(t, "dry run: "+stderr.String(), code, 0)
	if !strings.Contains(out.String(), "pi") {
		t.Fatal("missing executable preview")
	}
	if _, err := os.Stat(layout.ConfigDir); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote configuration: %v", err)
	}
}

func TestPiStartAcceptanceResumeRemainsNativeAfterSeparator(t *testing.T) {
	if _, err := parseStart([]string{"pi", "--resume"}); err == nil {
		t.Fatal("managed resume accepted for Pi")
	}
	opts := parseStartForTest(t, "pi", "--", "--resume", "session with spaces")
	if !slices.Equal(opts.Native, []string{"--resume", "session with spaces"}) {
		t.Fatal("native Pi resume changed")
	}
}
