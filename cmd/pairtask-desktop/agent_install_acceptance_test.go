//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type plistElement struct {
	XMLName  xml.Name
	Text     string         `xml:",chardata"`
	Children []plistElement `xml:",any"`
}

func (p plistElement) field(t *testing.T, key string) plistElement {
	t.Helper()
	for i := 0; i+1 < len(p.Children); i += 2 {
		if p.Children[i].XMLName.Local == "key" && p.Children[i].Text == key {
			return p.Children[i+1]
		}
	}
	t.Fatalf("plist lacks %q", key)
	return plistElement{}
}

func readInstalledPlist(t *testing.T, file string) plistElement {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Dict plistElement `xml:"dict"`
	}
	if err := xml.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p.Dict
}

type agentFixture struct {
	file, exe, dir, log string
	loaded              bool
}

func (f *agentFixture) checkInstalledPlist(t *testing.T) {
	t.Helper()
	dict := readInstalledPlist(t, f.file)
	wantFields := map[string]string{"Label": "ai.pairtask.desktop", "ProcessType": "Standard", "StandardOutPath": f.log, "StandardErrorPath": f.log}
	for k, want := range wantFields {
		if got := dict.field(t, k); got.XMLName.Local != "string" || got.Text != want {
			t.Fatalf("%s: got %+v, want %q", k, got, want)
		}
	}
	var args []string
	for _, a := range dict.field(t, "ProgramArguments").Children {
		args = append(args, a.Text)
	}
	if !slices.Equal(args, []string{f.exe, "daemon", "--dir", f.dir}) {
		t.Fatalf("ProgramArguments = %q", args)
	}
	if dict.field(t, "RunAtLoad").XMLName.Local != "true" || dict.field(t, "KeepAlive").field(t, "SuccessfulExit").XMLName.Local != "false" {
		t.Fatal("expected agent at login and restart on failure")
	}
}

func (f *agentFixture) launchctl(t *testing.T, args []string) ([]byte, error) {
	t.Helper()
	const target = "gui/501/ai.pairtask.desktop"
	switch {
	case slices.Equal(args, []string{"bootstrap", "gui/501", f.file}):
		if f.loaded {
			return nil, fmt.Errorf("duplicate service")
		}
		f.checkInstalledPlist(t)
		f.loaded = true
	case slices.Equal(args, []string{"bootout", target}):
		if !f.loaded {
			return []byte("Could not find service"), fmt.Errorf("not loaded")
		}
		f.loaded = false
	case slices.Equal(args, []string{"print", target}):
		if f.loaded {
			return []byte("state = running"), nil
		}
		return []byte("Could not find service"), fmt.Errorf("not loaded")
	default:
		t.Fatalf("unexpected launchctl args: %q", args)
	}
	return nil, nil
}

func checkAgentRunning(t *testing.T, env agentEnv) {
	t.Helper()
	var status bytes.Buffer
	if err := runAgent([]string{"status"}, env, &status); err != nil || strings.TrimSpace(status.String()) != "running" {
		t.Fatalf("status %q: %v", status.String(), err)
	}
}

func TestAgentInstallReplaceStatusAndUninstall(t *testing.T) {
	home := t.TempDir()
	f := agentFixture{
		exe:  filepath.Join(home, "Applications & Tools", "pairtask-desktop"),
		dir:  filepath.Join(home, "Data & Spaces"),
		file: filepath.Join(home, "Library", "LaunchAgents", "ai.pairtask.desktop.plist"),
		log:  filepath.Join(home, "Library", "Logs", "pairtask-desktop.log"),
	}
	env := agentEnv{goos: "darwin", uid: 501, home: home, exe: f.exe, launchctl: func(args ...string) ([]byte, error) {
		return f.launchctl(t, args)
	}}
	for range 2 {
		if err := runAgent([]string{"install", "--dir", f.dir}, env, io.Discard); err != nil {
			t.Fatal(err)
		}
		if !f.loaded {
			t.Fatal("install didn't load the agent")
		}
	}
	checkAgentRunning(t, env)
	if err := runAgent([]string{"uninstall"}, env, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.loaded {
		t.Fatal("uninstalled agent remains loaded")
	}
	if _, err := os.Stat(f.file); !os.IsNotExist(err) {
		t.Fatalf("plist after uninstall: %v", err)
	}
}

func TestAgentRefusesRootAndUnsupportedHost(t *testing.T) {
	for _, host := range []struct {
		os     string
		uid    int
		reason string
	}{{"darwin", 0, "root"}, {"linux", 501, "macos"}} {
		env := agentEnv{goos: host.os, uid: host.uid, home: t.TempDir(), exe: "/unused", launchctl: func(...string) ([]byte, error) { t.Fatal("unexpected launchctl"); return nil, nil }}
		for _, action := range []string{"install", "uninstall", "status"} {
			err := runAgent([]string{action}, env, io.Discard)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), host.reason) {
				t.Fatalf("%s on %s uid %d: %v", action, host.os, host.uid, err)
			}
		}
	}
}
