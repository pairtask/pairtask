package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/desktop"
)

// runningAgent answers launchctl as a loaded, running agent with pid; it
// records the calls.
type runningAgent struct {
	pid   string
	calls []string
}

func (r *runningAgent) run(args ...string) ([]byte, error) {
	r.calls = append(r.calls, strings.Join(args, " "))
	if args[0] == "print" {
		return []byte("gui/501/ai.pairtask.desktop = {\n\tstate = running\n\tpid = " + r.pid + "\n}\n"), nil
	}
	return nil, nil
}

func (r *runningAgent) restarted() bool {
	return strings.Contains(strings.Join(r.calls, "\n"), "bootstrap ")
}

// refreshEnv is an installed, running agent for a data folder, this build
// being version.
func refreshEnv(t *testing.T, r *runningAgent, version string) (agentEnv, string) {
	t.Helper()
	e := agentEnv{goos: "darwin", uid: 501, home: t.TempDir(), exe: "/Applications/Pairtask.app/Contents/MacOS/pairtask-desktop", version: version, launchctl: r.run}
	dir := t.TempDir()
	if err := e.writePlist(dir); err != nil {
		t.Fatal(err)
	}
	return e, dir
}

func recordOwner(t *testing.T, dir string, pid int, version string) {
	t.Helper()
	data, _ := json.Marshal(desktop.OwnerInfo{PID: pid, Role: "agent", Version: version, Since: time.Now()})
	if err := os.WriteFile(filepath.Join(dir, "owner.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func refresh(t *testing.T, e agentEnv) string {
	t.Helper()
	var out bytes.Buffer
	if err := runAgent([]string{"refresh"}, e, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestRefreshLeavesACurrentAgentAlone(t *testing.T) {
	r := &runningAgent{pid: "42"}
	e, dir := refreshEnv(t, r, "v0.0.0-new")
	recordOwner(t, dir, 42, "v0.0.0-new")
	if out := refresh(t, e); out != "running\n" || r.restarted() {
		t.Fatalf("a current agent: %q, restarted %v", out, r.restarted())
	}
}

func TestRefreshRestartsAnAgentThatPredatesTheOwnerRecord(t *testing.T) {
	r := &runningAgent{pid: "42"}
	e, _ := refreshEnv(t, r, "v0.0.0-new")
	if out := refresh(t, e); !strings.HasPrefix(out, "restarted:") || !r.restarted() {
		t.Fatalf("an agent with no owner record: %q, calls %v", out, r.calls)
	}
}

func TestRefreshRestartsAnAgentOfAnotherVersion(t *testing.T) {
	r := &runningAgent{pid: "42"}
	e, dir := refreshEnv(t, r, "v0.0.0-new")
	recordOwner(t, dir, 42, "v0.0.0-old")
	if out := refresh(t, e); !strings.Contains(out, "v0.0.0-old") || !r.restarted() {
		t.Fatalf("an older agent: %q", out)
	}
}

func TestRefreshRestartsAnAgentThatDoesntServeTheNode(t *testing.T) {
	r := &runningAgent{pid: "42"}
	e, dir := refreshEnv(t, r, "v0.0.0-new")
	recordOwner(t, dir, 77, "v0.0.0-new")
	if out := refresh(t, e); !strings.HasPrefix(out, "restarted:") {
		t.Fatalf("an agent that isn't the owner: %q", out)
	}
}

func TestRefreshOfADevBuildChecksNoVersion(t *testing.T) {
	r := &runningAgent{pid: "42"}
	e, dir := refreshEnv(t, r, "dev")
	recordOwner(t, dir, 42, "v0.0.0-whatever")
	if out := refresh(t, e); out != "running\n" {
		t.Fatalf("a dev build restarted a running agent: %q", out)
	}
}

func TestRefreshReportsAnAgentThatIsntInstalled(t *testing.T) {
	r := &runningAgent{pid: "42"}
	e := agentEnv{goos: "darwin", uid: 501, home: t.TempDir(), exe: "/x/pairtask-desktop", version: "v1", launchctl: r.run}
	if out := refresh(t, e); out != "not installed\n" || r.restarted() {
		t.Fatalf("no agent: %q", out)
	}
}

func TestRefreshChangesNothingWhenThePlistsFolderCantBeRead(t *testing.T) {
	r := &runningAgent{pid: "42"}
	e, _ := refreshEnv(t, r, "v0.0.0-new")
	plist, err := os.ReadFile(e.plistPath())
	if err != nil {
		t.Fatal(err)
	}
	odd := strings.Replace(string(plist), "<string>--dir</string>", "<string >--dir</string>", 1)
	if err := os.WriteFile(e.plistPath(), []byte(odd), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runAgent([]string{"refresh"}, e, &bytes.Buffer{}); err == nil {
		t.Fatal("a plist whose folder can't be read was refreshed")
	}
	if got, _ := os.ReadFile(e.plistPath()); string(got) != odd {
		t.Fatal("the plist was rewritten")
	}
	if strings.Contains(strings.Join(r.calls, "\n"), "bootout") {
		t.Fatalf("the running agent was stopped: %v", r.calls)
	}
}

func TestRefreshDoesntRestartOverAnUnreadableOwnerRecord(t *testing.T) {
	r := &runningAgent{pid: "42"}
	e, dir := refreshEnv(t, r, "v0.0.0-new")
	if err := os.WriteFile(filepath.Join(dir, "owner.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runAgent([]string{"refresh"}, e, &bytes.Buffer{}); err == nil || r.restarted() {
		t.Fatalf("an unreadable owner record: err %v, restarted %v", err, r.restarted())
	}
}
