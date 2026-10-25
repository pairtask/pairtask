package resume

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func bindingPath(dir, agent string) string {
	sum := sha256.Sum256([]byte(agent))
	return filepath.Join(dir, "codex-"+hex.EncodeToString(sum[:])[:16]+".json")
}

func TestResumeBindingAcceptancePrivateRoundTripAndReplacement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	store := Store{Dir: dir}
	want := Binding{AgentID: "../agent with spaces", ThreadID: "first", Cwd: "/actual/project", UpdatedAt: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	path := bindingPath(dir, want.AgentID)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("binding must exist privately at the hashed path: %v", err)
	}
	want.ThreadID = "second"
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(want.AgentID)
	if err != nil || got != want {
		t.Fatalf("binding round trip: got %+v, err %v", got, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("atomic save left extra files: %v, %v", files, err)
	}
}

func TestResumeBindingAcceptanceMissingCorruptAndWrongAgent(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if _, err := store.Load("alpha"); !errors.Is(err, ErrNoBinding) {
		t.Fatalf("missing binding: %v", err)
	}
	path := bindingPath(store.Dir, "alpha")
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("alpha"); !errors.Is(err, ErrCorruptBinding) {
		t.Fatalf("corrupt binding: %v", err)
	}
	data, _ := json.Marshal(Binding{AgentID: "beta", ThreadID: "other", Cwd: "/other", UpdatedAt: time.Now().UTC()})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("alpha"); !errors.Is(err, ErrAgentMismatch) {
		t.Fatalf("wrong agent binding accepted: %v", err)
	}
}

func TestResumeBindingAcceptanceFailedSavePreservesPreviousRecord(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	store := Store{Dir: t.TempDir()}
	before := Binding{AgentID: "alpha", ThreadID: "retained", Cwd: "/project", UpdatedAt: time.Now().UTC()}
	if err := store.Save(before); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(store.Dir, 0700) })
	after := before
	after.ThreadID = "must-not-replace"
	if err := store.Save(after); err == nil {
		t.Fatal("save unexpectedly succeeded in an unwritable directory")
	}
	got, err := store.Load("alpha")
	if err != nil || got != before {
		t.Fatalf("failed save destroyed previous binding: %+v, %v", got, err)
	}
}

func writeCandidate(t *testing.T, home, id, cwd string, modified time.Time) {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "10", "25")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": cwd}})
	path := filepath.Join(dir, "rollout-2026-10-25-"+id+".jsonl")
	if err := os.WriteFile(path, append(data, []byte("\nnot metadata; private conversation body\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func TestResumeBindingAcceptanceCandidatesPreferCwdThenRecencyAndStayBounded(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	writeCandidate(t, home, "outside-newest", "/elsewhere", now)
	writeCandidate(t, home, "matching-older", "/project", now.Add(-2*time.Hour))
	writeCandidate(t, home, "matching-newer", "/project", now.Add(-time.Hour))
	got, err := Candidates(home, "/project", 2)
	if err != nil || len(got) != 2 {
		t.Fatalf("candidate page: %+v, %v", got, err)
	}
	if got[0].ThreadID != "matching-newer" || got[1].ThreadID != "matching-older" {
		t.Fatalf("wrong candidate preference: %+v", got)
	}
	for _, candidate := range got {
		if candidate.Bytes <= 0 || candidate.Cwd != "/project" || candidate.Modified.IsZero() {
			t.Fatalf("missing candidate metadata: %+v", candidate)
		}
	}
}
