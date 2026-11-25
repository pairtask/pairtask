package main

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// launchctlScript answers each launchctl call by its verb: an error for
// the verbs in fail (each time), success otherwise; it records the calls.
type launchctlScript struct {
	fail  map[string]bool
	calls []string
}

func (l *launchctlScript) run(args ...string) ([]byte, error) {
	l.calls = append(l.calls, strings.Join(args, " "))
	if l.fail[args[0]] {
		return []byte(args[0] + " failed"), errors.New("exit 1")
	}
	return nil, nil
}

func replaceEnv(t *testing.T, l *launchctlScript) agentEnv {
	t.Helper()
	return agentEnv{goos: "darwin", uid: 501, home: t.TempDir(), exe: "/Applications/Pairtask.app/Contents/MacOS/pairtask-desktop", launchctl: l.run}
}

func plistText(t *testing.T, e agentEnv) string {
	t.Helper()
	b, err := os.ReadFile(e.plistPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A replacement whose new job won't start puts the previous plist back and
// starts the previous job again.
func TestAgentReplaceRestoresOnBootstrapFailure(t *testing.T) {
	ok := &launchctlScript{}
	e := replaceEnv(t, ok)
	if err := runAgent([]string{"install", "--dir", "/old/data"}, e, io.Discard); err != nil {
		t.Fatal(err)
	}
	failing := &launchctlScript{fail: map[string]bool{"bootstrap": true}}
	e.launchctl = failing.run
	err := runAgent([]string{"install", "--dir", "/new/data"}, e, io.Discard)
	if err == nil {
		t.Fatal("a failed bootstrap reported success")
	}
	if got := plistText(t, e); !strings.Contains(got, "/old/data") || strings.Contains(got, "/new/data") {
		t.Fatalf("the previous plist wasn't restored:\n%s", got)
	}
	if n := strings.Count(strings.Join(failing.calls, "\n"), "bootstrap "); n != 2 {
		t.Fatalf("bootstrap calls %d, want the new one and the restored one: %v", n, failing.calls)
	}
}

// A replacement that can't stop the running job changes nothing.
func TestAgentReplaceLeavesAJobItCantStop(t *testing.T) {
	ok := &launchctlScript{}
	e := replaceEnv(t, ok)
	if err := runAgent([]string{"install", "--dir", "/old/data"}, e, io.Discard); err != nil {
		t.Fatal(err)
	}
	stuck := &launchctlScript{fail: map[string]bool{"bootout": true}} // print succeeds: still loaded
	e.launchctl = stuck.run
	if err := runAgent([]string{"install", "--dir", "/new/data"}, e, io.Discard); err == nil {
		t.Fatal("replacing a job that couldn't be stopped reported success")
	}
	if got := plistText(t, e); !strings.Contains(got, "/old/data") {
		t.Fatalf("the running job's plist was changed:\n%s", got)
	}
}

// A first install whose job won't start leaves no plist behind.
func TestAgentFirstInstallCleansUpOnFailure(t *testing.T) {
	failing := &launchctlScript{fail: map[string]bool{"bootstrap": true, "bootout": true, "print": true}}
	e := replaceEnv(t, failing)
	if err := runAgent([]string{"install", "--dir", "/data"}, e, io.Discard); err == nil {
		t.Fatal("a failed first install reported success")
	}
	if _, err := os.Stat(e.plistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed first install left a plist: %v", err)
	}
}

// The agent doesn't take the MCP mode's --address setting.
func TestDaemonRefusesAddress(t *testing.T) {
	err := runDaemon([]string{"--dir", t.TempDir(), "--address", "box=10.0.0.2:7000"}, nil)
	if err == nil || !strings.Contains(err.Error(), "--address") {
		t.Fatalf("got %v, want --address refused", err)
	}
}
