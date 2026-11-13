package personkey

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
)

// Where the keys live. In the secured runtime the operator acts as root,
// and root's home is replaced with the container on every upgrade: there
// the keys live on secured's state volume, which survives it. Elsewhere
// they live in the account's state directory.

// SecuredStateDir is the secured runtime's state volume.
const SecuredStateDir = "/var/lib/collab-ai-secured"

// DirEnv names a key directory that overrides every default.
const DirEnv = "PAIRTASK_IDENTITY_DIR"

// DefaultDir is $PAIRTASK_IDENTITY_DIR; else, as root in the secured
// runtime, operator-identity on its state volume; else the account's
// state directory (UserDir).
func DefaultDir() (string, error) {
	return defaultDir(os.Getenv, os.Geteuid(), isDir)
}

func defaultDir(env func(string) string, euid int, isDir func(string) bool) (string, error) {
	dir, _, err := locate(env, euid, isDir)
	return dir, err
}

// locate is DefaultDir, and whether it is the secured runtime's volume:
// the one place keys an earlier release left in root's home move to.
func locate(env func(string) string, euid int, isDir func(string) bool) (string, bool, error) {
	if dir := env(DirEnv); dir != "" {
		return dir, false, nil
	}
	if euid == 0 && isDir(SecuredStateDir) {
		return filepath.Join(SecuredStateDir, "operator-identity"), true, nil
	}
	dir, err := userDir(env)
	return dir, false, err
}

// UserDir is $XDG_STATE_HOME/pairtask/identity, else
// ~/.local/state/pairtask/identity: where keys lived before the secured
// runtime kept them on its volume.
func UserDir() (string, error) { return userDir(os.Getenv) }

func userDir(env func(string) string) (string, error) {
	if state := env("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "pairtask", "identity"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "pairtask", "identity"), nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ResolveDir is DefaultDir. On the secured runtime's volume it first
// adopts keys an earlier release made in root's home, when the volume
// holds none yet, so they outlive the container; an explicit
// $PAIRTASK_IDENTITY_DIR never takes keys from elsewhere.
func ResolveDir() (string, error) {
	dir, secured, err := locate(os.Getenv, os.Geteuid(), isDir)
	if err != nil || !secured {
		return dir, err
	}
	legacy, err := UserDir()
	if err != nil {
		return dir, nil
	}
	_, err = AdoptLegacy(dir, legacy)
	return dir, err
}

// AdoptLegacy copies complete, private keys from legacy into dir when dir
// has none, and reports whether it did. legacy is left as it was; keys
// missing or incomplete there are not adopted.
func AdoptLegacy(dir, legacy string) (bool, error) {
	if _, err := Load(dir); !errors.Is(err, ErrNoKeys) {
		return false, nil
	}
	keys, err := Load(legacy)
	if errors.Is(err, ErrNoKeys) || errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := privateDir(dir); err != nil {
		return false, err
	}
	for i, key := range []ed25519.PrivateKey{keys.person, keys.device} {
		if err := create(keyFile(filepath.Join(dir, []string{personFile, deviceFile}[i])), key); err != nil {
			return false, err
		}
	}
	return true, nil
}
