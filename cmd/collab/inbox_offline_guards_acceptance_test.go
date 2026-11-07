package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestInboxOfflineAcceptanceExplicitDatabaseAndArchiveConfirmationRequired(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, missing := range []string{"database", "confirmation", "socket", "upper"} {
		f := newOfflineInbox(t)
		f.seed(t, offlineSeed{id: "must-survive"})
		t.Setenv("PAIRTASK_DB_PATH", f.database)
		t.Setenv("COLLAB_DB_PATH", f.database)
		before := offlineSnapshot(t, f.database)
		args := []string{"operator", "inbox", "archive", f.output}
		args = append(args, offlineInvalidArgs(f, missing)...)
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), args, &stdout, &stderr)
		if code == 0 {
			t.Fatalf("invalid maintenance request succeeded: %s", missing)
		}
		assertStatusEqual(t, "invalid request source", offlineSnapshot(t, f.database), before)
		_, err := os.Stat(f.output)
		assertStatusEqual(t, "invalid request publishes nothing", os.IsNotExist(err), true)
	}
}

func offlineInvalidArgs(f *offlineInboxFixture, missing string) []string {
	switch missing {
	case "database":
		return []string{"--confirm"}
	case "confirmation":
		return []string{"--database", f.database}
	case "upper":
		return []string{"--database", f.database, "--confirm", "--upper", "999"}
	}
	return []string{"--database", f.database, "--confirm", "--socket", "not-an-offline-option"}
}

func TestInboxOfflineAcceptanceMissingSourceNeverCreatesDatabase(t *testing.T) {
	f := newOfflineInbox(t)
	f.database = filepath.Join(filepath.Dir(f.database), "missing.db")
	for _, verb := range []string{"export", "archive"} {
		var flags []string
		if verb == "archive" {
			flags = []string{"--confirm"}
		}
		code, _, _ := f.call(t, verb, flags...)
		if code == 0 {
			t.Fatal("maintenance accepted a missing source")
		}
		_, err := os.Stat(f.database)
		assertStatusEqual(t, "no empty source database created", os.IsNotExist(err), true)
	}
}

func TestInboxOfflineAcceptanceOwnershipLockCoversSymlinkAliasAndDryRun(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "locked"})
	lock, err := os.OpenFile(f.database+".lock", os.O_CREATE|os.O_RDWR, 0600)
	offlineOK(t, err)
	defer lock.Close()
	offlineOK(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	alias := filepath.Join(filepath.Dir(f.output), "alias.db")
	offlineOK(t, os.Symlink(f.database, alias))
	f.database = alias
	before := offlineSnapshot(t, f.database)
	for _, mode := range []string{"export", "archive", "dry-run"} {
		offlineLocked(t, f, mode)
	}
	assertStatusEqual(t, "locked source unchanged", offlineSnapshot(t, f.database), before)
}

func offlineLocked(t *testing.T, f *offlineInboxFixture, mode string) {
	t.Helper()
	verb, flags := "archive", []string{"--confirm"}
	if mode == "export" {
		verb, flags = "export", nil
	}
	if mode == "dry-run" {
		flags = []string{"--dry-run"}
	}
	code, _, _ := f.call(t, verb, flags...)
	if code == 0 {
		t.Fatalf("%s bypassed the source ownership lock", mode)
	}
	_, err := os.Stat(f.output)
	assertStatusEqual(t, "locked maintenance publishes nothing", os.IsNotExist(err), true)
}

func TestInboxOfflineAcceptanceHardlinkedSourceAndSymlinkDestinationRefused(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "private"})
	link := filepath.Join(filepath.Dir(f.database), "hardlink.db")
	offlineOK(t, os.Link(f.database, link))
	code, _, _ := f.call(t, "export")
	if code == 0 {
		t.Fatal("maintenance accepted a multiply-linked source")
	}
	offlineOK(t, os.Remove(link))
	protected := filepath.Join(filepath.Dir(f.output), "protected")
	offlineOK(t, os.WriteFile(protected, []byte("keep"), 0600))
	offlineOK(t, os.Symlink(protected, f.output))
	before := offlineSnapshot(t, f.database)
	code, _, _ = f.call(t, "archive", "--confirm")
	if code == 0 {
		t.Fatal("archive followed an existing destination symlink")
	}
	data, err := os.ReadFile(protected)
	offlineOK(t, err)
	assertStatusEqual(t, "symlink target unchanged", string(data), "keep")
	assertStatusEqual(t, "failed publication source unchanged", offlineSnapshot(t, f.database), before)
}
