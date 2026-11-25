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

func checkInstalledPlist(t *testing.T, file, exe, dir, log string) {
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
	wantFields := map[string]string{"Label": "ai.pairtask.desktop", "ProcessType": "Standard", "StandardOutPath": log, "StandardErrorPath": log}
	for k, want := range wantFields {
		if got := p.Dict.field(t, k); got.XMLName.Local != "string" || got.Text != want {
			t.Fatalf("%s: got %+v, want %q", k, got, want)
		}
	}
	var args []string
	for _, a := range p.Dict.field(t, "ProgramArguments").Children {
		args = append(args, a.Text)
	}
	if !slices.Equal(args, []string{exe, "daemon", "--dir", dir}) {
		t.Fatalf("ProgramArguments = %q", args)
	}
	if p.Dict.field(t, "RunAtLoad").XMLName.Local != "true" || p.Dict.field(t, "KeepAlive").field(t, "SuccessfulExit").XMLName.Local != "false" {
		t.Fatal("expected agent at login and restart on failure")
	}
}

func TestAgentInstallReplaceStatusAndUninstall(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(home, "Applications & Tools", "pairtask-desktop")
	dir := filepath.Join(home, "Data & Spaces")
	file := filepath.Join(home, "Library", "LaunchAgents", "ai.pairtask.desktop.plist")
	domain, target := "gui/501", "gui/501/ai.pairtask.desktop"
	loaded := false
	env := agentEnv{goos: "darwin", uid: 501, home: home, exe: exe, launchctl: func(args ...string) ([]byte, error) {
		switch {
		case slices.Equal(args, []string{"bootstrap", domain, file}):
			if loaded {
				return nil, fmt.Errorf("duplicate service")
			}
			checkInstalledPlist(t, file, exe, dir, filepath.Join(home, "Library", "Logs", "pairtask-desktop.log"))
			loaded = true
		case slices.Equal(args, []string{"bootout", target}):
			if !loaded {
				return []byte("Could not find service"), fmt.Errorf("not loaded")
			}
			loaded = false
		case slices.Equal(args, []string{"print", target}):
			if loaded {
				return []byte("state = running"), nil
			}
			return []byte("Could not find service"), fmt.Errorf("not loaded")
		default:
			t.Fatalf("unexpected launchctl args: %q", args)
		}
		return nil, nil
	}}
	for range 2 {
		if err := runAgent([]string{"install", "--dir", dir}, env, io.Discard); err != nil {
			t.Fatal(err)
		}
		if !loaded {
			t.Fatal("install didn't load the agent")
		}
	}
	var status bytes.Buffer
	if err := runAgent([]string{"status"}, env, &status); err != nil || strings.TrimSpace(status.String()) != "running" {
		t.Fatalf("status %q: %v", status.String(), err)
	}
	if err := runAgent([]string{"uninstall"}, env, io.Discard); err != nil {
		t.Fatal(err)
	}
	if loaded {
		t.Fatal("uninstalled agent remains loaded")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
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
