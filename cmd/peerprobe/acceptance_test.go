package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
			dir := t.TempDir()
			if _, err := loadIdentity(identityPaths{dir: dir, name: "gate"}); err != nil {
				t.Fatal(err)
			}
			certPath, keyPath := filepath.Join(dir, "gate.crt"), filepath.Join(dir, "gate.key")
			switch damage {
			case "missing-key":
				if err := os.Remove(keyPath); err != nil {
					t.Fatal(err)
				}
			case "missing-cert":
				if err := os.Remove(certPath); err != nil {
					t.Fatal(err)
				}
			case "corrupt-key":
				if err := os.WriteFile(keyPath, []byte("damaged key"), 0600); err != nil {
					t.Fatal(err)
				}
			case "corrupt-cert":
				if err := os.WriteFile(certPath, []byte("damaged certificate"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			beforeCert, certErr := os.ReadFile(certPath)
			beforeKey, keyErr := os.ReadFile(keyPath)
			if _, err := loadIdentity(identityPaths{dir: dir, name: "gate"}); err == nil {
				t.Fatal("broken identity silently replaced")
			}
			afterCert, afterCertErr := os.ReadFile(certPath)
			afterKey, afterKeyErr := os.ReadFile(keyPath)
			if !bytes.Equal(beforeCert, afterCert) || !bytes.Equal(beforeKey, afterKey) {
				t.Fatal("existing identity bytes changed")
			}
			if os.IsNotExist(certErr) != os.IsNotExist(afterCertErr) || os.IsNotExist(keyErr) != os.IsNotExist(afterKeyErr) {
				t.Fatal("missing identity material regenerated")
			}
		})
	}
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
