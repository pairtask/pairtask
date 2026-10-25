package main

import (
	"cmp"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/pairtask/pairtask/internal/host"
	"github.com/pairtask/pairtask/internal/resume"
)

// resumeStateDir is where bindings live: $XDG_STATE_HOME/pairtask, by
// default ~/.local/state/pairtask.
func resumeStateDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	return filepath.Join(dir, "pairtask")
}

// recordThread remembers the owned conversation for `pairtask start codex
// --resume`. A failed save only warns; the live session goes on.
func recordThread(agent string, store resume.Store) func(host.Thread) {
	return func(t host.Thread) {
		wd, _ := os.Getwd()
		b := resume.Binding{AgentID: agent, ThreadID: t.ID, Cwd: cmp.Or(t.Cwd, wd), UpdatedAt: time.Now().UTC()}
		if err := store.Save(b); err != nil {
			log.Printf("warning: couldn't record the Codex conversation for --resume: %v", err)
		}
	}
}
