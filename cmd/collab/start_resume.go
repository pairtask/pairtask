package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/resume"
)

// statusFunc reads the broker's status; status.Fetch in production.
type statusFunc func(ctx context.Context, socket string) (protocol.StatusSnapshot, error)

// resumeCandidates bounds the conversations suggested when none is recorded.
const resumeCandidates = 10

// errStartRefused means start already explained why it won't launch.
var errStartRefused = errors.New("start refused")

// managedSelector reports native arguments that mean "this agent's
// conversation": exactly `resume` or `resume --last`. Codex's own picker and
// latest-in-cwd don't work through the managed launcher (#67), so these use
// the recorded binding instead. Any other form is Codex's to interpret.
func managedSelector(native []string) bool {
	return slices.Equal(native, []string{"resume"}) || slices.Equal(native, []string{"resume", "--last"})
}

// nativeLifecycle reports native arguments that already choose a
// conversation, which --resume can't be combined with.
func nativeLifecycle(native []string) bool {
	return slices.Contains(native, "resume") || slices.Contains(native, "fork")
}

// resolveResume prepares a Codex start: --resume becomes `resume <thread>`
// from the agent's binding, and an explicit `resume ID` that is really a
// pairtask session ID is refused. It returns the directory to start in, if
// any.
func resolveResume(opts *startOptions, rt startRuntime) (string, error) {
	if opts.Agent != "codex" {
		return "", nil
	}
	if opts.Resume {
		return resumeBinding(opts, rt)
	}
	return "", refuseBrokerID(opts, rt)
}

// resumeBinding loads the agent's conversation, refuses while the agent is
// still connected, and appends `resume <thread>` to the native arguments.
func resumeBinding(opts *startOptions, rt startRuntime) (string, error) {
	b, err := (resume.Store{Dir: rt.Layout.StateDir}).Load(opts.ID)
	if errors.Is(err, resume.ErrNoBinding) {
		fmt.Fprintf(rt.Stderr, "no recorded Codex conversation for %q.\n", opts.ID)
		printCandidates(rt, opts.ID)
		return "", errStartRefused
	}
	if err != nil {
		fmt.Fprintf(rt.Stderr, "can't resume %q: %v\nRestart without --resume, or resume an explicit conversation ID.\n", opts.ID, err)
		return "", errStartRefused
	}
	if err := refuseConnected(opts, rt); err != nil {
		return "", err
	}
	opts.Native = append(opts.Native, "resume", b.ThreadID)
	dir := b.Cwd
	if _, err := os.Stat(dir); dir == "" || err != nil {
		fmt.Fprintf(rt.Stderr, "warning: the recorded directory %q is gone; resuming in the current directory\n", b.Cwd)
		dir = ""
	}
	fmt.Fprintf(rt.Stderr, "resuming %s: thread %s in %s\n", opts.ID, b.ThreadID, cmp.Or(dir, "the current directory"))
	return dir, nil
}

// refuseConnected refuses while a session of the agent is connected, even
// when the snapshot is degraded for an unrelated reason. Without a broker
// it only warns: the launcher's registration still refuses a duplicate.
func refuseConnected(opts *startOptions, rt startRuntime) error {
	snap, err := brokerStatus(opts, rt)
	for _, s := range snap.Sessions {
		if s.AgentID == opts.ID && s.State == "transport_connected" {
			fmt.Fprintf(rt.Stderr, "%s is still connected (session %s); close it first.\n", opts.ID, s.SessionID)
			return errStartRefused
		}
	}
	if !snap.Reachable {
		fmt.Fprintf(rt.Stderr, "warning: couldn't check whether %s is still connected (%v); the broker refuses a duplicate ID\n", opts.ID, err)
	}
	return nil
}

// refuseBrokerID refuses `resume ID` when ID is a pairtask session ID, the
// usual mix-up; anything else goes to Codex unchanged.
func refuseBrokerID(opts *startOptions, rt startRuntime) error {
	if len(opts.Native) < 2 || opts.Native[0] != "resume" || rt.Status == nil {
		return nil
	}
	snap, _ := brokerStatus(opts, rt)
	for _, s := range snap.Sessions {
		if slices.Contains(opts.Native[1:], s.SessionID) {
			fmt.Fprintf(rt.Stderr, "%s is a pairtask session ID, not a Codex conversation ID.\n", s.SessionID)
			printCandidates(rt, opts.ID)
			return errStartRefused
		}
	}
	return nil
}

func brokerStatus(opts *startOptions, rt startRuntime) (protocol.StatusSnapshot, error) {
	if rt.Status == nil {
		return protocol.UnavailableStatus(), errors.New("no status reader")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return rt.Status(ctx, cmp.Or(opts.Socket, brokerSocketDefault))
}

// printCandidates suggests saved conversations, never choosing one.
func printCandidates(rt startRuntime, agent string) {
	wd, _ := os.Getwd()
	found, err := resume.Candidates(rt.Layout.CodexHome, wd, resumeCandidates)
	if err != nil || len(found) == 0 {
		fmt.Fprintln(rt.Stderr, "No saved Codex conversations found to suggest.")
		return
	}
	fmt.Fprintln(rt.Stderr, "Saved Codex conversations, this directory's first, then newest:")
	for _, c := range found {
		fmt.Fprintf(rt.Stderr, "  %s  %s  %d KiB  %s\n    pairtask start codex --id %s -- resume %s\n",
			c.Modified.Local().Format("2006-01-02 15:04"), c.Cwd, c.Bytes>>10, c.ThreadID, shellQuote(agent), shellQuote(c.ThreadID))
	}
}

// printDir reports where a dry run would start.
func printDir(dir string, stdout io.Writer) {
	if dir != "" {
		fmt.Fprintln(stdout, "cwd: "+dir)
	}
}
