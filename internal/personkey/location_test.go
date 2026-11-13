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
	assertDefaultDir(t, dirCase{env, 0, secured, filepath.Join(SecuredStateDir, "operator-identity")})
	accountDir := filepath.Join(state, "pairtask", "identity")
	assertDefaultDir(t, dirCase{env, 1000, secured, accountDir})
	assertDefaultDir(t, dirCase{env, 0, func(string) bool { return false }, accountDir})

	override := filepath.Join(t.TempDir(), "specific-identity")
	withOverride := func(name string) string {
		if name == DirEnv {
			return override
		}
		return env(name)
	}
	assertDefaultDir(t, dirCase{withOverride, 0, secured, override})
}

type dirCase struct {
	env   func(string) string
	euid  int
	isDir func(string) bool
	want  string
}

func assertDefaultDir(t *testing.T, tc dirCase) {
	t.Helper()
	got, err := defaultDir(tc.env, tc.euid, tc.isDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != tc.want {
		t.Fatalf("got %q, want %q", got, tc.want)
	}
}

func TestAdoptLegacyKeepsIdentityAndPrivateModes(t *testing.T) {
	base := t.TempDir()
	legacy, dest := filepath.Join(base, "legacy"), filepath.Join(base, "secured", "operator-identity")
	created := mustCreate(t, legacy)
	before := readKeyPair(t, legacy)
	assertAdopted(t, dest, legacy, true)
	loaded, err := Load(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(created.PersonPublic(), loaded.PersonPublic()) {
		t.Fatal("adoption changed the operator's person identity")
	}
	assertPrivateMode(t, dest, 0700)
	for _, name := range []string{personFile, deviceFile} {
		assertSameBytes(t, filepath.Join(dest, name), before[name])
		assertPrivateMode(t, filepath.Join(dest, name), 0600)
		assertSameBytes(t, filepath.Join(legacy, name), before[name])
	}
	assertAdopted(t, dest, legacy, false)
}

func TestAdoptLegacyDoesNotReplaceExistingIdentity(t *testing.T) {
	base := t.TempDir()
	legacy, dest := filepath.Join(base, "legacy"), filepath.Join(base, "dest")
	oldKeys := mustCreate(t, legacy)
	newKeys := mustCreate(t, dest)
	assertAdopted(t, dest, legacy, false)
	loaded, err := Load(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.PersonPublic(), newKeys.PersonPublic()) {
		t.Fatal("destination identity changed")
	}
	if bytes.Equal(loaded.PersonPublic(), oldKeys.PersonPublic()) {
		t.Fatal("destination unexpectedly uses legacy key")
	}
}

func TestAdoptLegacyDoesNotRepairPartialIdentity(t *testing.T) {
	base := t.TempDir()
	legacy, partial, dest := filepath.Join(base, "legacy"), filepath.Join(base, "partial"), filepath.Join(base, "dest")
	mustCreate(t, legacy)
	if err := os.Mkdir(partial, 0700); err != nil {
		t.Fatal(err)
	}
	person := mustReadFile(t, filepath.Join(legacy, personFile))
	if err := os.WriteFile(filepath.Join(partial, personFile), person, 0600); err != nil {
		t.Fatal(err)
	}
	adopted, err := AdoptLegacy(dest, partial)
	if adopted || !errors.Is(err, ErrIncomplete) {
		t.Fatalf("partial identity should not be adopted: adopted=%v, err=%v", adopted, err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial identity created destination: %v", err)
	}
}

func mustCreate(t *testing.T, dir string) *Keys {
	t.Helper()
	keys, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func readKeyPair(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	return map[string][]byte{
		personFile: mustReadFile(t, filepath.Join(dir, personFile)),
		deviceFile: mustReadFile(t, filepath.Join(dir, deviceFile)),
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertAdopted(t *testing.T, dest, legacy string, want bool) {
	t.Helper()
	got, err := AdoptLegacy(dest, legacy)
	if err != nil || got != want {
		t.Fatalf("adopted=%v, err=%v; want %v", got, err, want)
	}
}

func assertPrivateMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode %04o, want %04o", path, got, want)
	}
}

func assertSameBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	if got := mustReadFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("%s changed during adoption", path)
	}
}
