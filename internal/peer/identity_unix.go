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
	p := identityPaths{dir: dir, key: filepath.Join(dir, identityFile)}
	if err := p.ensureDir(); err != nil {
		return nil, err
	}
	known, initialized, err := marker.IdentityFingerprint(ctx)
	if err != nil {
		return nil, fmt.Errorf("read identity marker: %w", err)
	}
	if initialized {
		return p.load(known)
	}
	id, err := p.adoptOrCreate(random)
	if err != nil {
		return nil, err
	}
	if err := marker.InitIdentity(ctx, id.fingerprint); err != nil {
		return nil, fmt.Errorf("record identity: %w", err)
	}
	return id, nil
}

// identityPaths are the private key directory and the key file in it.
type identityPaths struct {
	dir, key string
}

// load reads the key of an initialized workspace; it never creates one.
func (p identityPaths) load(known string) (*WorkspaceIdentity, error) {
	id, err := p.read(false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrIdentityMissing
	}
	if err != nil {
		return nil, err
	}
	if id.fingerprint != known {
		return nil, ErrIdentityMismatch
	}
	return id, nil
}

// adoptOrCreate is the key of a workspace with no marker yet: one already
// published (by a crashed or concurrent creator) or a new one. Either way it
// is made durable, file and directory entries, before the marker records it.
func (p identityPaths) adoptOrCreate(random io.Reader) (*WorkspaceIdentity, error) {
	id, err := p.read(true)
	if errors.Is(err, fs.ErrNotExist) {
		id, err = p.create(random)
	}
	if err != nil {
		return nil, err
	}
	return id, p.syncPublished()
}

// ensureDir creates the directory 0700 if missing, whatever the umask, and
// checks it is a private directory of ours that we can use, not a symlink.
func (p identityPaths) ensureDir() error {
	err := os.Mkdir(p.dir, 0o700)
	switch {
	case err == nil:
		if err := p.restoreDirMode(); err != nil {
			return err
		}
	case !errors.Is(err, fs.ErrExist):
		return fmt.Errorf("create identity directory: %w", err)
	}
	info, err := os.Lstat(p.dir)
	if err != nil {
		return fmt.Errorf("identity directory: %w", err)
	}
	if !info.IsDir() || !privateDir(info) {
		return fmt.Errorf("%w: %s must be a directory of ours with mode 0700", ErrIdentityUnsafe, p.dir)
	}
	return nil
}

// restoreDirMode gives a directory we just created the 0700 a umask such as
// 0117 took away. It changes only the directory it opened, without following
// a symlink, and only when that is a directory of ours.
func (p identityPaths) restoreDirMode() error {
	fd, err := syscall.Open(p.dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("%w: %s changed while it was created: %v", ErrIdentityUnsafe, p.dir, err)
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return fmt.Errorf("identity directory: %w", err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR || int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%w: %s must be a directory of ours", ErrIdentityUnsafe, p.dir)
	}
	if err := syscall.Fchmod(fd, 0o700); err != nil {
		return fmt.Errorf("create identity directory: %w", err)
	}
	return nil
}

// privateDir reports a directory of ours that only we can use, and can.
func privateDir(info fs.FileInfo) bool {
	return private(info) && info.Mode().Perm() == 0o700
}

// private reports a file owned by us that no one else can use.
func private(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return false
	}
	return info.Mode().Perm()&0o077 == 0
}

// open opens the key without following a symlink and without blocking on a
// FIFO, and checks what it opened before anything reads it.
func (p identityPaths) open() (*os.File, error) {
	fd, err := syscall.Open(p.key, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, p.openError(err)
	}
	f := os.NewFile(uintptr(fd), p.key)
	if err := p.checkOpened(f, fd); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (p identityPaths) openError(err error) error {
	switch {
	case errors.Is(err, syscall.ENOENT):
		return fs.ErrNotExist
	case errors.Is(err, syscall.ELOOP):
		return fmt.Errorf("%w: %s is a symlink", ErrIdentityUnsafe, p.key)
	}
	return fmt.Errorf("open identity key: %w", err)
}

// checkOpened accepts a private regular file and makes reads blocking again.
func (p identityPaths) checkOpened(f *os.File, fd int) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("identity key: %w", err)
	}
	if !info.Mode().IsRegular() || !private(info) {
		return fmt.Errorf("%w: %s must be a regular file of ours with mode 0600", ErrIdentityUnsafe, p.key)
	}
	return syscall.SetNonblock(fd, false)
}

// read loads the key; sync also flushes the file before it is relied on.
func (p identityPaths) read(sync bool) (*WorkspaceIdentity, error) {
	f, err := p.open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxIdentityBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read identity key: %w", err)
	}
	id, err := parseIdentity(data)
	if err != nil {
		return nil, err
	}
	if sync {
		return id, f.Sync()
	}
	return id, nil
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

// create publishes a new key without ever replacing one: a link fails if the
// name exists, so a concurrent creator's key wins and is read instead.
func (p identityPaths) create(random io.Reader) (*WorkspaceIdentity, error) {
	encoded, err := newKeyPEM(random)
	if err != nil {
		return nil, err
	}
	tmp, err := p.writeTemp(encoded)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, p.key); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("publish identity key: %w", err)
	}
	return p.read(true)
}

func newKeyPEM(random io.Reader) ([]byte, error) {
	_, key, err := ed25519.GenerateKey(random)
	if err != nil {
		return nil, fmt.Errorf("generate identity key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// writeTemp writes data to a new private file beside the key and syncs it.
func (p identityPaths) writeTemp(data []byte) (string, error) {
	f, err := os.CreateTemp(p.dir, identityFile+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("write identity key: %w", err)
	}
	if err := writeSynced(f, data); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write identity key: %w", err)
	}
	return f.Name(), nil
}

// writeSynced writes, syncs and closes f.
func writeSynced(f *os.File, data []byte) error {
	_, err := f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// syncPublished makes the key's directory entry and the directory's own
// entry in its parent durable before the marker can record the identity.
func (p identityPaths) syncPublished() error {
	for _, d := range []string{p.dir, filepath.Dir(p.dir)} {
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
