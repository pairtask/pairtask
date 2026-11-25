package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
)

// The always-on agent as a macOS login item: a per-user LaunchAgent that
// runs `pairtask-desktop daemon`. ProcessType Standard keeps macOS from
// throttling it the way it naps background apps; KeepAlive restarts it if
// it fails.

const agentLabel = "ai.pairtask.desktop"

const agentUsage = `usage: pairtask-desktop agent install [--dir DIR] | uninstall | status`

// agentEnv is what agent needs from the host; tests replace it.
type agentEnv struct {
	goos      string
	uid       int
	home      string
	exe       string
	launchctl func(args ...string) ([]byte, error)
}

func hostAgentEnv() agentEnv {
	home, _ := os.UserHomeDir()
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return agentEnv{goos: runtime.GOOS, uid: os.Getuid(), home: home, exe: exe,
		launchctl: func(args ...string) ([]byte, error) { return exec.Command("launchctl", args...).CombinedOutput() }}
}

func (e agentEnv) plistPath() string {
	return filepath.Join(e.home, "Library", "LaunchAgents", agentLabel+".plist")
}

func (e agentEnv) domain() string { return "gui/" + strconv.Itoa(e.uid) }

// runAgent installs, removes or reports the login item.
func runAgent(args []string, env agentEnv, out io.Writer) error {
	if err := env.supported(); err != nil {
		return err
	}
	switch first(args) {
	case "install":
		return env.install(args[1:], out)
	case "uninstall":
		return env.uninstall(out)
	case "status":
		return env.status(out)
	}
	return errors.New(agentUsage)
}

// supported: macOS, as the user, never as root (the agent's data belongs
// to the user, and a root-owned one would lock them out of it).
func (e agentEnv) supported() error {
	if e.goos != "darwin" {
		return fmt.Errorf("agent: login items are macOS only for now; run `pairtask-desktop daemon` under your own service manager")
	}
	if e.uid == 0 {
		return errors.New("agent: don't run this as root or with sudo; run it as yourself")
	}
	if e.home == "" || e.exe == "" {
		return errors.New("agent: can't tell the home directory or this program's path")
	}
	return nil
}

func (e agentEnv) install(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("agent install", flag.ContinueOnError)
	dirFlag := fs.String("dir", "", "the data directory (default: the user config directory's pairtask-desktop)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := dataDir(*dirFlag)
	if err != nil {
		return err
	}
	if dir, err = filepath.Abs(dir); err != nil { // launchd starts it elsewhere
		return err
	}
	if err := e.writePlist(dir); err != nil {
		return err
	}
	_, _ = e.launchctl("bootout", e.domain()+"/"+agentLabel) // a loaded older one; none is fine
	if b, err := e.launchctl("bootstrap", e.domain(), e.plistPath()); err != nil {
		return fmt.Errorf("agent: launchctl bootstrap: %s", strings.TrimSpace(string(b)))
	}
	fmt.Fprintf(out, "Installed: pairtask runs in the background from now on, also after login (%s).\n", e.plistPath())
	return nil
}

func (e agentEnv) writePlist(dir string) error {
	if err := os.MkdirAll(filepath.Dir(e.plistPath()), 0o755); err != nil {
		return err
	}
	logs := filepath.Join(e.home, "Library", "Logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return err
	}
	plist, err := agentPlist(agentSpec{Exe: e.exe, Dir: dir, Log: filepath.Join(logs, "pairtask-desktop.log")})
	if err != nil {
		return err
	}
	return os.WriteFile(e.plistPath(), plist, 0o644)
}

func (e agentEnv) uninstall(out io.Writer) error {
	if err := e.stop(); err != nil {
		return err // the plist stays, so the state remains the one status reports
	}
	if err := os.Remove(e.plistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintln(out, "Removed: pairtask no longer runs in the background; Claude Desktop's extension runs it while open.")
	return nil
}

// stop unloads the agent. Not loaded already is fine; still loaded after a
// failed bootout is an error.
func (e agentEnv) stop() error {
	b, err := e.launchctl("bootout", e.domain()+"/"+agentLabel)
	if err == nil {
		return nil
	}
	if _, loaded := e.launchctl("print", e.domain()+"/"+agentLabel); loaded != nil {
		return nil // it wasn't loaded
	}
	return fmt.Errorf("agent: couldn't stop the background agent: %s", strings.TrimSpace(string(b)))
}

func (e agentEnv) status(out io.Writer) error {
	if _, err := os.Stat(e.plistPath()); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "not installed")
		return nil
	}
	b, err := e.launchctl("print", e.domain()+"/"+agentLabel)
	switch {
	case err != nil:
		fmt.Fprintln(out, "installed, not loaded: run `pairtask-desktop agent install` again")
	case bytes.Contains(b, []byte("state = running")):
		fmt.Fprintln(out, "running")
	default:
		fmt.Fprintln(out, "installed, not running")
	}
	return nil
}

// agentSpec is what the LaunchAgent runs: this program as the agent for
// a data directory, logging to a file.
type agentSpec struct {
	Exe, Dir, Log string
}

var plistTemplate = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.Exe}}</string>
		<string>daemon</string>
		<string>--dir</string>
		<string>{{.Dir}}</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ProcessType</key>
	<string>Standard</string>
	<key>StandardOutPath</key>
	<string>{{.Log}}</string>
	<key>StandardErrorPath</key>
	<string>{{.Log}}</string>
</dict>
</plist>
`))

// agentPlist is the LaunchAgent for s, every value XML-escaped.
func agentPlist(s agentSpec) ([]byte, error) {
	var b bytes.Buffer
	err := plistTemplate.Execute(&b, struct{ Label, Exe, Dir, Log string }{
		xmlText(agentLabel), xmlText(s.Exe), xmlText(s.Dir), xmlText(s.Log)})
	return b.Bytes(), err
}

func xmlText(v string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(v))
	return b.String()
}
