package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/pairtask/pairtask/internal/desktop"
)

// refresh is status, after restarting a running agent that isn't this
// build: an app update replaces the program on disk, but launchd keeps the
// process the previous version started until logout. The agent is put back
// with this program, as install would; a current agent is left alone.
func (e agentEnv) refresh(out io.Writer) error {
	state := e.state()
	if state != "running" {
		fmt.Fprintln(out, state)
		return nil
	}
	plist, err := os.ReadFile(e.plistPath())
	if err != nil {
		return err
	}
	dir := installedDir(plist)
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("agent refresh: can't tell the data folder from %s; nothing was changed. Run `pairtask-desktop agent install` to set it up again", e.plistPath())
	}
	why, err := e.stale(dir)
	if err != nil {
		return fmt.Errorf("agent refresh: %w; nothing was changed", err)
	}
	if why == "" {
		fmt.Fprintln(out, "running")
		return nil
	}
	if err := e.replace(dir); err != nil {
		return err
	}
	fmt.Fprintf(out, "restarted: %s; it runs this version now\n", why)
	return nil
}

// stale is why the running agent isn't this build, or "" when it is. The
// node's owner record says which process runs it and its version; an agent
// from before that record has none. A record that can't be read is an
// error, never a reason to restart.
func (e agentEnv) stale(dir string) (string, error) {
	owner, ok, err := desktop.ReadOwnerInfo(dir)
	switch {
	case err != nil:
		return "", fmt.Errorf("the node's owner record can't be read: %w", err)
	case !ok:
		return "the running agent predates this version (it records no owner)", nil
	case e.notOwner(owner.PID):
		return "the running agent isn't the copy serving this node", nil
	case e.version != "dev" && owner.Version != e.version:
		return fmt.Sprintf("the running agent was version %s", orUnknown(owner.Version)), nil
	}
	return "", nil
}

// notOwner: launchctl names the agent's process, and it isn't pid.
func (e agentEnv) notOwner(pid int) bool {
	agent := e.agentPID()
	return agent != 0 && agent != pid
}

// pidLine is launchctl print's line for the job's process.
var pidLine = regexp.MustCompile(`(?m)^\s*pid = (\d+)\s*$`)

// agentPID is the running agent's process ID, or 0 if launchctl doesn't say.
func (e agentEnv) agentPID() int {
	b, err := e.launchctl("print", e.domain()+"/"+agentLabel)
	if err != nil {
		return 0
	}
	m := pidLine.FindSubmatch(b)
	if m == nil {
		return 0
	}
	pid, _ := strconv.Atoi(string(m[1]))
	return pid
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
