package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"
)

type joinReport struct {
	Args []string
	Cwd  string
	PID  int
	Env  map[string]string
}

// The same test binary stands in for the CLI and then the opaque harness.
// Successful join replaces the CLI process; no shell or real model is used.
func TestPiJoinProcessHelper(t *testing.T) {
	if os.Getenv("PAIRTASK_JOIN_TEST_HELPER") != "1" {
		return
	}
	sep := slices.Index(os.Args, "--")
	if sep < 0 {
		os.Exit(91)
	}
	args := os.Args[sep+1:]
	if len(args) == 0 {
		os.Exit(91)
	}
	if args[0] == "harness" {
		os.Exit(runJoinHarness(args[1:]))
	}
	os.Exit(run(context.Background(), args, os.Stdout, os.Stderr))
}

func runJoinHarness(args []string) int {
	if code := writeJoinReport(args[1:]); code != 0 {
		return code
	}
	if args[0] == "signal" {
		for {
			time.Sleep(time.Hour)
		}
	}
	code, err := strconv.Atoi(args[0])
	if err != nil {
		return 94
	}
	return code
}

func writeJoinReport(args []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		return 92
	}
	report := joinReport{Args: args, Cwd: cwd, PID: os.Getpid(), Env: map[string]string{}}
	for _, key := range []string{"PAIRTASK_AGENT_ID", "PAIRTASK_SOCKET", "PAIRTASK_ROOMS", "PAIRTASK_MCP", "PAIRTASK_JOIN_UNRELATED"} {
		report.Env[key] = os.Getenv(key)
	}
	if json.NewEncoder(os.Stdout).Encode(report) != nil {
		return 93
	}
	return 0
}

func joinHelper(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestPiJoinProcessHelper$", "--", "join", "--id", "generic-agent", "--socket", "/tmp/join socket", "--room", "alpha", "--room", "beta", "--", executable, "-test.run=^TestPiJoinProcessHelper$", "--", "harness", mode, "a space", "literal $value; $(nothing)", "--room", "native-room"}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = append(os.Environ(), "PAIRTASK_JOIN_TEST_HELPER=1", "PAIRTASK_JOIN_UNRELATED=kept")
	cmd.Dir = t.TempDir()
	return cmd
}

func checkJoinReport(t *testing.T, cmd *exec.Cmd, r joinReport) {
	t.Helper()
	assertStartEqual(t, "opaque argv", r.Args, []string{"a space", "literal $value; $(nothing)", "--room", "native-room"})
	assertStartEqual(t, "working directory", r.Cwd, cmd.Dir)
	assertStartEqual(t, "exec keeps process identity", r.PID, cmd.Process.Pid)
	assertStartEqual(t, "agent identity", r.Env["PAIRTASK_AGENT_ID"], "generic-agent")
	assertStartEqual(t, "socket", r.Env["PAIRTASK_SOCKET"], "/tmp/join socket")
	assertStartEqual(t, "unrelated environment", r.Env["PAIRTASK_JOIN_UNRELATED"], "kept")
	var rooms []string
	if err := json.Unmarshal([]byte(r.Env["PAIRTASK_ROOMS"]), &rooms); err != nil {
		t.Fatal(err)
	}
	assertStartEqual(t, "rooms JSON", rooms, []string{"alpha", "beta"})
	if r.Env["PAIRTASK_MCP"] == "" {
		t.Fatal("missing bridge executable")
	}
}

func TestPiJoinAcceptancePreservesArgumentsEnvironmentCwdAndExit(t *testing.T) {
	cmd := joinHelper(t, "37")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil || cmd.ProcessState.ExitCode() != 37 {
		t.Fatalf("harness exit not preserved: %v (%s)", err, stderr.String())
	}
	var report joinReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	checkJoinReport(t, cmd, report)
}

func TestPiJoinAcceptanceSignalReachesTheExecutedHarness(t *testing.T) {
	cmd := joinHelper(t, "signal")
	report := startJoinHarness(t, cmd)
	checkJoinReport(t, cmd, report)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("signal did not terminate harness")
	}
	state, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("signal status unavailable: %v", cmd.ProcessState)
	}
	assertStartEqual(t, "harness terminated by signal", state.Signaled(), true)
	assertStartEqual(t, "original signal preserved", state.Signal(), syscall.SIGTERM)
}

func startJoinHarness(t *testing.T, cmd *exec.Cmd) joinReport {
	t.Helper()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	var report joinReport
	if err = json.NewDecoder(output).Decode(&report); err != nil {
		_ = cmd.Wait()
		t.Fatalf("harness not ready: %v (%s)", err, stderr.String())
	}
	return report
}

func TestPiJoinAcceptanceRejectsIncompleteOrInvalidContract(t *testing.T) {
	for _, args := range [][]string{
		{"join", "--id", "operator", "--", "unused"},
		{"join", "--id", "worker", "--room", "square", "--", "unused"},
		{"join", "--id", "worker", "--room", "bad/name", "--", "unused"},
		{"join", "--id", "worker", "--"},
	} {
		var out, stderr bytes.Buffer
		assertStartEqual(t, "invalid join", run(context.Background(), args, &out, &stderr), 2)
	}
}
