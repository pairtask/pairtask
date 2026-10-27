//go:build unix

package peer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

const (
	identityFile     = "identity.key"
	maxIdentityBytes = 4096
)

// LoadOrCreateIdentity loads the workspace key from dir, creating it only for
// a workspace that never had one:
//
//  1. dir must be a private directory of ours; it is created 0700 if missing;
//  2. the marker is read; failing to read it creates nothing;
//  3. an existing key file must be a private regular file of ours holding one
//     Ed25519 key, and must match the marker, which it initializes if unset
//     (recovering a crash between publishing the key and recording it);
//  4. a missing key with a marker is ErrIdentityMissing;
//  5. otherwise a new key is written to a private temporary file and linked
//     into place, which never replaces an existing file: a concurrent creator
//     that wins is loaded instead.
//
// Nothing ever deletes, rewrites or replaces a key file.
func LoadOrCreateIdentity(ctx context.Context, dir string, marker IdentityMarker, random io.Reader) (*WorkspaceIdentity, error) {
	if dir == "" {
		return nil, ErrIdentityUnavailable
	}
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	known, initialized, err := marker.IdentityFingerprint(ctx)
	if err != nil {
		return nil, fmt.Errorf("read identity marker: %w", err)
	}
	path := filepath.Join(dir, identityFile)
	// Before the marker exists, whatever key is adopted is made durable first,
	// even one another creator published and may not have synced yet.
	id, err := readIdentity(path, !initialized)
	if errors.Is(err, fs.ErrNotExist) {
		if initialized {
			return nil, ErrIdentityMissing
		}
		id, err = createIdentity(dir, path, random)
	}
	if err != nil {
		return nil, err
	}
	if initialized && id.fingerprint != known {
		return nil, ErrIdentityMismatch
	}
	if !initialized {
		if err := syncPublished(dir); err != nil {
			return nil, err
		}
	}
	if err := marker.InitIdentity(ctx, id.fingerprint); err != nil {
		return nil, fmt.Errorf("record identity: %w", err)
	}
	return id, nil
}

// privateDir creates dir 0700 if missing and checks it is a private
// directory of ours, not a symlink.
func privateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create identity directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("identity directory: %w", err)
	}
	if !info.IsDir() || !private(info) {
		return fmt.Errorf("%w: %s must be a directory of ours with mode 0700", ErrIdentityUnsafe, dir)
	}
	return nil
}

// private reports a file owned by us that no one else can use.
func private(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return false
	}
	return info.Mode().Perm()&0o077 == 0
}

// readIdentity opens without following a symlink and without blocking on a
// FIFO, then checks what it opened before reading it; sync also flushes it.
func readIdentity(path string, sync bool) (*WorkspaceIdentity, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, syscall.ENOENT):
		return nil, fs.ErrNotExist
	case errors.Is(err, syscall.ELOOP):
		return nil, fmt.Errorf("%w: %s is a symlink", ErrIdentityUnsafe, path)
	case err != nil:
		return nil, fmt.Errorf("open identity key: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("identity key: %w", err)
	}
	if !info.Mode().IsRegular() || !private(info) {
		return nil, fmt.Errorf("%w: %s must be a regular file of ours with mode 0600", ErrIdentityUnsafe, path)
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		return nil, fmt.Errorf("identity key: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxIdentityBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read identity key: %w", err)
	}
	id, err := parseIdentity(data)
	if err == nil && sync {
		err = f.Sync()
	}
	return id, err
}

func parseIdentity(data []byte) (*WorkspaceIdentity, error) {
	if len(data) > maxIdentityBytes {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrIdentityCorrupt, maxIdentityBytes)
	}
	block, rest := pem.Decode(data)
	if !onePrivateKeyBlock(block, rest) {
		return nil, fmt.Errorf("%w: want one PKCS#8 PEM block", ErrIdentityCorrupt)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIdentityCorrupt, err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: not an Ed25519 key", ErrIdentityCorrupt)
	}
	return newIdentity(key)
}

// onePrivateKeyBlock reports a PRIVATE KEY block with nothing after it.
func onePrivateKeyBlock(block *pem.Block, rest []byte) bool {
	if block == nil || block.Type != "PRIVATE KEY" {
		return false
	}
	return len(bytes.TrimSpace(rest)) == 0
}

// createIdentity publishes a new key without ever replacing one: a link
// fails if the name exists, so a concurrent creator's key wins and is read.
func createIdentity(dir, path string, random io.Reader) (*WorkspaceIdentity, error) {
	_, key, err := ed25519.GenerateKey(random)
	if err != nil {
		return nil, fmt.Errorf("generate identity key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	tmp, err := writeTemp(dir, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, path); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("publish identity key: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return nil, err
	}
	return readIdentity(path, true)
}

// writeTemp writes data to a new private file in dir and syncs it.
func writeTemp(dir string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, identityFile+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("write identity key: %w", err)
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write identity key: %w", err)
	}
	return f.Name(), nil
}

// syncPublished makes the key's directory entry and the directory's own
// entry in its parent durable before the marker can record the identity.
func syncPublished(dir string) error {
	for _, d := range []string{dir, filepath.Dir(dir)} {
		if err := syncDir(d); err != nil {
			return fmt.Errorf("sync identity directory: %w", err)
		}
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
