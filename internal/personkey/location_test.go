package personkey

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDirUsesPersistentSecuredVolume(t *testing.T) {
	state := t.TempDir()
	env := func(name string) string {
		if name == "XDG_STATE_HOME" {
			return state
		}
		return ""
	}
	secured := func(path string) bool { return path == SecuredStateDir }
	got, err := defaultDir(env, 0, secured)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(SecuredStateDir, "operator-identity"); got != want {
		t.Fatalf("root in secured: got %q, want %q", got, want)
	}
	for _, tc := range []struct {
		name  string
		euid  int
		isDir func(string) bool
	}{
		{"non-root", 1000, secured},
		{"no secured volume", 0, func(string) bool { return false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := defaultDir(env, tc.euid, tc.isDir)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(state, "pairtask", "identity"); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
	override := filepath.Join(t.TempDir(), "specific-identity")
	got, err = defaultDir(func(name string) string {
		if name == DirEnv {
			return override
		}
		return env(name)
	}, 0, secured)
	if err != nil || got != override {
		t.Fatalf("explicit directory: got %q, %v; want %q", got, err, override)
	}
}

func TestAdoptLegacyKeepsIdentityAndPrivateModes(t *testing.T) {
	base := t.TempDir()
	legacy, dest := filepath.Join(base, "legacy"), filepath.Join(base, "secured", "operator-identity")
	created, err := Create(legacy)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, name := range []string{personFile, deviceFile} {
		before[name], err = os.ReadFile(filepath.Join(legacy, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	adopted, err := AdoptLegacy(dest, legacy)
	if err != nil || !adopted {
		t.Fatalf("adopted=%v, err=%v", adopted, err)
	}
	loaded, err := Load(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(created.PersonPublic(), loaded.PersonPublic()) {
		t.Fatal("adoption changed the operator's person identity")
	}
	info, err := os.Stat(dest)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("destination directory mode: %v, %v", info, err)
	}
	for _, name := range []string{personFile, deviceFile} {
		path := filepath.Join(dest, name)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, before[name]) {
			t.Fatalf("%s changed during adoption: %v", name, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s mode: %v, %v", name, info, err)
		}
		old, err := os.ReadFile(filepath.Join(legacy, name))
		if err != nil || !bytes.Equal(old, before[name]) {
			t.Fatalf("legacy %s changed: %v", name, err)
		}
	}
	adopted, err = AdoptLegacy(dest, legacy)
	if err != nil || adopted {
		t.Fatalf("second adoption should do nothing: adopted=%v, err=%v", adopted, err)
	}
}

func TestAdoptLegacyNeverReplacesExistingOrPartialIdentity(t *testing.T) {
	base := t.TempDir()
	legacy, dest := filepath.Join(base, "legacy"), filepath.Join(base, "dest")
	oldKeys, err := Create(legacy)
	if err != nil {
		t.Fatal(err)
	}
	newKeys, err := Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := AdoptLegacy(dest, legacy)
	if err != nil || adopted {
		t.Fatalf("existing identity overwritten: adopted=%v, err=%v", adopted, err)
	}
	loaded, err := Load(dest)
	if err != nil || !bytes.Equal(loaded.PersonPublic(), newKeys.PersonPublic()) {
		t.Fatalf("existing identity changed: %v", err)
	}
	if bytes.Equal(loaded.PersonPublic(), oldKeys.PersonPublic()) {
		t.Fatal("destination unexpectedly uses legacy key")
	}
	partial := filepath.Join(base, "partial")
	if err := os.Mkdir(partial, 0700); err != nil {
		t.Fatal(err)
	}
	person, err := os.ReadFile(filepath.Join(legacy, personFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, personFile), person, 0600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(base, "missing")
	adopted, err = AdoptLegacy(missing, partial)
	if adopted || !errors.Is(err, ErrIncomplete) {
		t.Fatalf("partial identity should be reported without adoption: adopted=%v, err=%v", adopted, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial identity created destination: %v", err)
	}
}
