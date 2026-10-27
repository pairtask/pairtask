//go:build unix

package peer_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

type identityMarker struct {
	mu                sync.Mutex
	fp                string
	readErr, writeErr error
}

func (m *identityMarker) IdentityFingerprint(context.Context) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fp, m.fp != "", m.readErr
}
func (m *identityMarker) InitIdentity(_ context.Context, fp string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.writeErr != nil {
		return m.writeErr
	}
	if m.fp != "" && m.fp != fp {
		return peer.ErrIdentityMismatch
	}
	m.fp = fp
	return nil
}
func identityLoad(t *testing.T, dir string, marker peer.IdentityMarker, entropy io.Reader) *peer.WorkspaceIdentity {
	t.Helper()
	id, err := peer.LoadOrCreateIdentity(context.Background(), dir, marker, entropy)
	invOK(t, err)
	if id == nil {
		t.Fatal("identity missing after successful load")
	}
	return id
}
func identityBytes(t *testing.T, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "identity.key"))
	invOK(t, err)
	return data
}
func identityAbsent(t *testing.T, dir string) {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, "identity.key"))
	invEqual(t, errors.Is(err, os.ErrNotExist), true)
}
func identityRefused(t *testing.T, dir string, marker peer.IdentityMarker, want error) {
	t.Helper()
	id, err := peer.LoadOrCreateIdentity(context.Background(), dir, marker, rand.Reader)
	invEqual(t, id == nil, true)
	if !errors.Is(err, want) {
		t.Fatalf("identity error = %v, want %v", err, want)
	}
}

func TestIdentityAcceptancePersistentPrivateKeyAndRedaction(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "peer")
	marker := &identityMarker{}
	id := identityLoad(t, dir, marker, rand.Reader)
	invOK(t, peer.ValidIdentityKey(id.Fingerprint()))
	data := identityBytes(t, dir)
	block, rest := pem.Decode(data)
	if block == nil {
		t.Fatal("identity is not PEM")
	}
	invEqual(t, block.Type, "PRIVATE KEY")
	invEqual(t, len(bytes.TrimSpace(rest)), 0)
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	invOK(t, err)
	key, ok := parsed.(ed25519.PrivateKey)
	invEqual(t, ok, true)
	spki, err := x509.MarshalPKIXPublicKey(key.Public())
	invOK(t, err)
	invEqual(t, id.Fingerprint(), fmt.Sprintf("sha256:%x", sha256.Sum256(spki)))
	for _, path := range []string{dir, filepath.Join(dir, "identity.key")} {
		info, err := os.Stat(path)
		invOK(t, err)
		invEqual(t, info.Mode().Perm()&0077, os.FileMode(0))
	}
	info, err := os.Stat(filepath.Join(dir, "identity.key"))
	invOK(t, err)
	invEqual(t, info.Mode().Perm(), os.FileMode(0600))
	loaded := identityLoad(t, dir, marker, bytes.NewReader(nil))
	invEqual(t, loaded.Fingerprint(), id.Fingerprint())
	invEqual(t, bytes.Equal(identityBytes(t, dir), data), true)
	encoded, err := json.Marshal(id)
	invOK(t, err)
	for _, text := range []string{string(encoded), fmt.Sprintf("%v", id), fmt.Sprintf("%+v", id), fmt.Sprintf("%#v", id)} {
		invEqual(t, strings.Contains(text, string(data)), false)
		invEqual(t, strings.Contains(text, fmt.Sprint([]byte(key))), false)
		invEqual(t, strings.Contains(text, fmt.Sprintf("%x", []byte(key))), false)
	}
}

func TestIdentityAcceptanceMissingCorruptAndChangedKeysNeverRegenerate(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "changed"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "peer")
			marker := &identityMarker{}
			original := identityLoad(t, dir, marker, rand.Reader).Fingerprint()
			path := filepath.Join(dir, "identity.key")
			want := peer.ErrIdentityMissing
			switch kind {
			case "missing":
				invOK(t, os.Remove(path))
			case "corrupt":
				invOK(t, os.WriteFile(path, []byte("broken key"), 0600))
				want = peer.ErrIdentityCorrupt
			case "changed":
				other := filepath.Join(t.TempDir(), "peer")
				identityLoad(t, other, &identityMarker{}, rand.Reader)
				invOK(t, os.WriteFile(path, identityBytes(t, other), 0600))
				want = peer.ErrIdentityMismatch
			}
			before, _ := os.ReadFile(path)
			identityRefused(t, dir, marker, want)
			after, _ := os.ReadFile(path)
			invEqual(t, bytes.Equal(before, after), true)
			fp, found, err := marker.IdentityFingerprint(context.Background())
			invOK(t, err)
			invEqual(t, found, true)
			invEqual(t, fp, original)
			if kind == "missing" {
				identityAbsent(t, dir)
			}
		})
	}
}

func TestIdentityAcceptanceRecoversFileBeforeMarkerCommit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "peer")
	marker := &identityMarker{writeErr: errors.New("marker write unavailable")}
	_, err := peer.LoadOrCreateIdentity(context.Background(), dir, marker, rand.Reader)
	invError(t, err)
	before := identityBytes(t, dir)
	marker.writeErr = nil
	id := identityLoad(t, dir, marker, bytes.NewReader(nil))
	invEqual(t, bytes.Equal(before, identityBytes(t, dir)), true)
	fp, found, err := marker.IdentityFingerprint(context.Background())
	invOK(t, err)
	invEqual(t, found, true)
	invEqual(t, fp, id.Fingerprint())
}

func TestIdentityAcceptanceFailedReadsAndEntropyDoNotPublish(t *testing.T) {
	for _, kind := range []string{"marker read", "entropy"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "peer")
			marker := &identityMarker{}
			var entropy io.Reader = bytes.NewReader(nil)
			if kind == "marker read" {
				marker.readErr = errors.New("marker unavailable")
				entropy = rand.Reader
			}
			_, err := peer.LoadOrCreateIdentity(context.Background(), dir, marker, entropy)
			invError(t, err)
			identityAbsent(t, dir)
			invEqual(t, marker.fp, "")
		})
	}
}

func TestIdentityAcceptanceConcurrentCreatorsConverge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "peer")
	marker := &identityMarker{}
	start := make(chan struct{})
	type result struct {
		fp  string
		err error
	}
	results := make(chan result, 16)
	for range 16 {
		go func() {
			<-start
			id, err := peer.LoadOrCreateIdentity(context.Background(), dir, marker, rand.Reader)
			fp := ""
			if id != nil {
				fp = id.Fingerprint()
			}
			results <- result{fp, err}
		}()
	}
	close(start)
	first := <-results
	invOK(t, first.err)
	invEqual(t, first.fp != "", true)
	for range 15 {
		r := <-results
		invOK(t, r.err)
		invEqual(t, r.fp, first.fp)
	}
	invEqual(t, identityLoad(t, dir, marker, bytes.NewReader(nil)).Fingerprint(), first.fp)
}

func TestIdentityAcceptanceRejectsUnsafePathsAndPermissions(t *testing.T) {
	for _, kind := range []string{"directory symlink", "key symlink", "key directory", "key mode", "directory mode"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "peer")
			marker := &identityMarker{}
			identityLoad(t, dir, marker, rand.Reader)
			path := filepath.Join(dir, "identity.key")
			switch kind {
			case "directory symlink":
				invOK(t, os.Rename(dir, filepath.Join(base, "real")))
				invOK(t, os.Symlink(filepath.Join(base, "real"), dir))
			case "key symlink":
				invOK(t, os.Rename(path, filepath.Join(dir, "real.key")))
				invOK(t, os.Symlink("real.key", path))
			case "key directory":
				invOK(t, os.Remove(path))
				invOK(t, os.Mkdir(path, 0700))
			case "key mode":
				invOK(t, os.Chmod(path, 0644))
			case "directory mode":
				invOK(t, os.Chmod(dir, 0755))
			}
			identityRefused(t, dir, marker, peer.ErrIdentityUnsafe)
		})
	}
}

func TestIdentityAcceptanceFIFORefusedWithoutBlocking(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "peer")
	invOK(t, os.Mkdir(dir, 0700))
	path := filepath.Join(dir, "identity.key")
	invOK(t, syscall.Mkfifo(path, 0600))
	done := make(chan error, 1)
	go func() {
		_, err := peer.LoadOrCreateIdentity(context.Background(), dir, &identityMarker{}, rand.Reader)
		done <- err
	}()
	select {
	case err := <-done:
		invEqual(t, errors.Is(err, peer.ErrIdentityUnsafe), true)
	case <-time.After(time.Second):
		// Unblock an incorrect blocking reader so this regression cannot leak
		// a goroutine into the remainder of the suite.
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			syscall.Close(fd)
		}
		t.Fatal("identity loader blocked on a FIFO")
	}
}

func TestIdentityAcceptanceOversizedOrPartialKeyIsPreserved(t *testing.T) {
	for _, data := range [][]byte{[]byte("-----BEGIN PRIVATE KEY-----\npartial"), bytes.Repeat([]byte{'x'}, 4097)} {
		dir := filepath.Join(t.TempDir(), "peer")
		invOK(t, os.Mkdir(dir, 0700))
		invOK(t, os.WriteFile(filepath.Join(dir, "identity.key"), data, 0600))
		_, err := peer.LoadOrCreateIdentity(context.Background(), dir, &identityMarker{}, rand.Reader)
		invError(t, err)
		invEqual(t, bytes.Equal(data, identityBytes(t, dir)), true)
	}
}

func TestIdentityAcceptanceCertificateRenewalPreservesSPKIPin(t *testing.T) {
	id := identityLoad(t, filepath.Join(t.TempDir(), "peer"), &identityMarker{}, rand.Reader)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	var firstDER []byte
	for n := range 2 {
		cert, err := id.Certificate("workspace", now.Add(time.Duration(n)*time.Hour), 24*time.Hour)
		invOK(t, err)
		if len(cert.Certificate) == 0 {
			t.Fatal("certificate chain missing")
		}
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		invOK(t, err)
		invEqual(t, leaf.PublicKeyAlgorithm, x509.Ed25519)
		invOK(t, leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature))
		invEqual(t, peer.SPKIFingerprint(leaf), id.Fingerprint())
		invEqual(t, peer.CertificateHasIdentity(leaf, id.Fingerprint()), true)
		invEqual(t, peer.CertificateHasIdentity(leaf, "sha256:"+strings.Repeat("0", 64)), false)
		if n == 0 {
			firstDER = cert.Certificate[0]
		} else {
			invEqual(t, bytes.Equal(firstDER, cert.Certificate[0]), false)
		}
	}
	other := identityLoad(t, filepath.Join(t.TempDir(), "peer"), &identityMarker{}, rand.Reader)
	invEqual(t, other.Fingerprint() != id.Fingerprint(), true)
	invEqual(t, peer.SPKIFingerprint(nil), "")
	invEqual(t, peer.SPKIFingerprint(&x509.Certificate{}), "")
	invEqual(t, peer.CertificateHasIdentity(nil, id.Fingerprint()), false)
	for _, key := range []string{"", strings.Repeat("a", 64), "SHA256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("g", 64)} {
		invError(t, peer.ValidIdentityKey(key))
	}
}
