package personkey_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pairtask/pairtask/internal/personkey"
)

func TestPersonKeyAcceptancePartialLossNeverCreatesReplacementIdentity(t *testing.T) {
	for _, missing := range []string{"person.key", "device.key"} {
		t.Run(missing, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "identity")
			if _, err := personkey.Create(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(dir, missing)); err != nil {
				t.Fatal(err)
			}
			if _, err := personkey.Load(dir); err == nil {
				t.Fatal("loading a partial identity silently created a replacement key")
			}
			if _, err := personkey.Create(dir); err == nil {
				t.Fatal("initialization silently rotated a partial identity")
			}
			if _, err := os.Lstat(filepath.Join(dir, missing)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing key was silently regenerated: %v", err)
			}
		})
	}
}

func TestPersonKeyAcceptanceMissingIdentityIsNotCreatedByLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "identity")
	if _, err := personkey.Load(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load without initialization: %v", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load created identity directory: %v", err)
	}
}

func TestPersonKeyAcceptancePrivateFilesRequiredForSigning(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "identity")
	if _, err := personkey.Create(dir); err != nil {
		t.Fatal(err)
	}
	requireKeyMode(t, dir, 0o700)
	for _, name := range []string{"person.key", "device.key"} {
		requireKeyMode(t, filepath.Join(dir, name), 0o600)
	}
	if _, err := personkey.Load(dir); err != nil {
		t.Fatalf("private identity did not load: %v", err)
	}
}

func requireKeyMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode is %o, want %o", path, info.Mode().Perm(), want)
	}
}

func TestPersonKeyAcceptanceUnsafeFileCannotBeUsed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "identity")
	if _, err := personkey.Create(dir); err != nil {
		t.Fatal(err)
	}
	device := filepath.Join(dir, "device.key")
	if err := os.Chmod(device, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := personkey.Load(dir); !errors.Is(err, personkey.ErrUnsafe) {
		t.Fatalf("world-readable device key was loaded: %v", err)
	}
	if err := os.Chmod(device, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := personkey.Load(dir); err != nil {
		t.Fatalf("restored private key did not load: %v", err)
	}
	assertSymlinkedKeyRejected(t, dir, device)
}

func assertSymlinkedKeyRejected(t *testing.T, dir, device string) {
	t.Helper()
	linked := filepath.Join(dir, "linked-device")
	if err := os.Rename(device, linked); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linked, device); err != nil {
		t.Fatal(err)
	}
	if _, err := personkey.Load(dir); !errors.Is(err, personkey.ErrUnsafe) {
		t.Fatalf("symlinked device key was loaded: %v", err)
	}
}
