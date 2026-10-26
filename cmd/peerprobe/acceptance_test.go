package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestAcceptanceIdentitySurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	first, err := loadIdentity(identityPaths{dir: dir, name: "gate"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadIdentity(identityPaths{dir: dir, name: "gate"})
	if err != nil {
		t.Fatal(err)
	}
	if peer.Fingerprint(first.Leaf) != peer.Fingerprint(second.Leaf) {
		t.Fatal("identity changed on reopen")
	}
	key, err := os.Stat(filepath.Join(dir, "gate.key"))
	if err != nil {
		t.Fatal(err)
	}
	if key.Mode().Perm()&0077 != 0 {
		t.Fatal("private key readable outside its owner")
	}
}

func TestAcceptanceBrokenIdentityIsNotSilentlyReplaced(t *testing.T) {
	for _, damage := range []string{"missing-key", "missing-cert", "corrupt-key", "corrupt-cert"} {
		t.Run(damage, func(t *testing.T) {
			paths := identityPaths{dir: t.TempDir(), name: "gate"}
			_, err := loadIdentity(paths)
			probeNoError(t, err)
			probeNoError(t, damageIdentity(paths, damage))
			before := readIdentityBytes(paths)
			if _, err := loadIdentity(paths); err == nil {
				t.Fatal("broken identity silently replaced")
			}
			after := readIdentityBytes(paths)
			if !before.equal(after) {
				t.Fatal("existing identity bytes or missing-file state changed")
			}
		})
	}
}

func probeNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func damageIdentity(paths identityPaths, damage string) error {
	cert, key := filepath.Join(paths.dir, "gate.crt"), filepath.Join(paths.dir, "gate.key")
	switch damage {
	case "missing-key":
		return os.Remove(key)
	case "missing-cert":
		return os.Remove(cert)
	case "corrupt-key":
		return os.WriteFile(key, []byte("damaged key"), 0600)
	case "corrupt-cert":
		return os.WriteFile(cert, []byte("damaged certificate"), 0600)
	default:
		return fmt.Errorf("unknown damage fixture %s", damage)
	}
}

type identityBytes struct {
	cert, key             []byte
	certAbsent, keyAbsent bool
}

func readIdentityBytes(paths identityPaths) identityBytes {
	cert, certErr := os.ReadFile(filepath.Join(paths.dir, "gate.crt"))
	key, keyErr := os.ReadFile(filepath.Join(paths.dir, "gate.key"))
	return identityBytes{cert: cert, key: key, certAbsent: os.IsNotExist(certErr), keyAbsent: os.IsNotExist(keyErr)}
}

func (b identityBytes) equal(other identityBytes) bool {
	return reflect.DeepEqual(b, other)
}

func TestAcceptanceGatePreservesNonSocketPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-socket")
	content := []byte("pre-existing file")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runGate(ctx, []string{"--socket", path, "--state", filepath.Join(dir, "state")}); err == nil {
		t.Fatal("occupied non-socket path accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("existing file damaged: %q,%v", got, err)
	}
}
