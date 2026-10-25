package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/host"
	"github.com/pairtask/pairtask/internal/resume"
)

func TestThreadBindingAcceptanceRecordsActualDirectoryAndFallback(t *testing.T) {
	store := resume.Store{Dir: t.TempDir()}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{t.TempDir(), ""} {
		before := time.Now().UTC()
		recordThread("reviewer", store)(host.Thread{ID: "owned-thread", Cwd: cwd})
		b, err := store.Load("reviewer")
		wantCwd := cwd
		if wantCwd == "" {
			wantCwd = wd
		}
		if err != nil || b.ThreadID != "owned-thread" || b.Cwd != wantCwd || b.UpdatedAt.Before(before) {
			t.Fatalf("owned thread was not recorded: %+v, %v", b, err)
		}
	}
}

func TestThreadBindingAcceptanceSaveFailureWarnsAndReturns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&diagnostic)
	t.Cleanup(func() { log.SetOutput(previous) })
	recordThread("reviewer", resume.Store{Dir: path})(host.Thread{ID: "still-live"})
	if !strings.Contains(diagnostic.String(), "warning") || !strings.Contains(diagnostic.String(), "--resume") {
		t.Fatalf("save failure lacked an actionable warning: %s", diagnostic.String())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("failed save changed existing data: %q, %v", data, err)
	}
}
