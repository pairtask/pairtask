//go:build linux || darwin

package main

import "testing"

func TestDesktopNotifyFlagAcceptanceDefaultAndOff(t *testing.T) {
	dir := t.TempDir()
	on, err := parseFlags([]string{"--dir", dir})
	if err != nil {
		t.Fatal(err)
	}
	if on.Notify == nil {
		t.Fatal("notifications were not enabled by default")
	}
	off, err := parseFlags([]string{"--dir", dir, "--notify=false"})
	if err != nil {
		t.Fatal(err)
	}
	if off.Notify != nil {
		t.Fatal("--notify=false left notifications enabled")
	}
}
