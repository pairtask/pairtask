// Package personkey keeps an operator's keys (#115): the person key, the
// root of what the operator signs, and the device key the operator's own
// clients sign messages with, certified by the person key. Both live in a
// private directory of the operator's account; no agent and no broker ever
// holds them.
package personkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/signing"
)

const (
	personFile = "person.key"
	deviceFile = "device.key"
	// deviceValidity is how long a device certificate lasts; it is renewed
	// renewBefore its end, in memory, on the next seal.
	deviceValidity = 90 * 24 * time.Hour
	renewBefore    = 7 * 24 * time.Hour
	// clockSlack backdates a fresh certificate, so a message stamped just
	// before it by a slower clock still falls inside it.
	clockSlack = time.Minute
)

// ErrUnsafe: the key directory or a key file isn't private to this account.
var ErrUnsafe = errors.New("personkey: not private to this account")

// Keys are an operator's person and device keys.
type Keys struct {
	person ed25519.PrivateKey
	device ed25519.PrivateKey
	mu     sync.Mutex
	cert   signing.Cert
}

// DefaultDir is $XDG_STATE_HOME/pairtask/identity, else
// ~/.local/state/pairtask/identity.
func DefaultDir() (string, error) {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "pairtask", "identity"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "pairtask", "identity"), nil
}

// ErrNoKeys: this account has no keys yet; ErrIncomplete: one of the two
// is missing, which is never repaired by making a new one, since a new key
// is a key change the operator approves (pairtask operator identity init).
var (
	ErrNoKeys     = fmt.Errorf("personkey: no keys yet: %w", os.ErrNotExist)
	ErrIncomplete = errors.New("personkey: one of the person and device keys is missing")
)

// Load loads this account's keys from dir and creates nothing.
func Load(dir string) (*Keys, error) {
	person, perr := load(keyFile(filepath.Join(dir, personFile)))
	device, derr := load(keyFile(filepath.Join(dir, deviceFile)))
	switch missing(perr, derr) {
	case 2:
		return nil, ErrNoKeys
	case 1:
		return nil, fmt.Errorf("%w in %s", ErrIncomplete, dir)
	}
	if err := errors.Join(perr, derr); err != nil {
		return nil, err
	}
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	return &Keys{person: person, device: device}, nil
}

// missing counts the key files that don't exist.
func missing(errs ...error) int {
	n := 0
	for _, err := range errs {
		if errors.Is(err, os.ErrNotExist) {
			n++
		}
	}
	return n
}

// ErrExists: Create found keys already there.
var ErrExists = errors.New("personkey: keys exist already")

// Create makes this account's person and device keys in dir, created
// 0700, only when neither exists.
func Create(dir string) (*Keys, error) {
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	keys := make([]ed25519.PrivateKey, 2)
	for i, name := range []string{personFile, deviceFile} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w in %s", ErrExists, dir)
		}
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		keys[i] = key
	}
	for i, name := range []string{personFile, deviceFile} {
		if err := create(keyFile(filepath.Join(dir, name)), keys[i]); err != nil {
			return nil, err
		}
	}
	return &Keys{person: keys[0], device: keys[1]}, nil
}

// PersonPublic is the person key, which the operator's workspace pins.
func (k *Keys) PersonPublic() ed25519.PublicKey { return k.person.Public().(ed25519.PublicKey) }

// Seal signs h with the device key, certified for h's workspace.
func (k *Keys) Seal(h signing.Header) signing.Seal {
	k.mu.Lock()
	cert := k.certFor(h.Workspace, h.CreatedAt)
	k.mu.Unlock()
	return signing.Sign(k.device, []signing.Cert{cert}, k.PersonPublic(), h)
}

// certFor is a device certificate valid for workspace at at, renewed when
// it is for another workspace, not yet valid or near its end.
func (k *Keys) certFor(workspace string, at time.Time) signing.Cert {
	if covers(k.cert, workspace, at) {
		return k.cert
	}
	k.cert = signing.Issue(k.person, signing.Cert{Kind: signing.KindDevice, Subject: signing.OperatorID,
		Key: k.device.Public().(ed25519.PublicKey), Workspace: workspace, NotBefore: at.Add(-clockSlack), NotAfter: at.Add(deviceValidity)})
	return k.cert
}

// covers: c certifies the device for workspace at at, and isn't near its end.
func covers(c signing.Cert, workspace string, at time.Time) bool {
	return c.Workspace == workspace && !at.Before(c.NotBefore) && at.Add(renewBefore).Before(c.NotAfter)
}

// privateDir creates dir 0700 if missing and accepts only a directory of
// ours, not a symlink, that no one else can use.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !(info.IsDir() && private(info)) {
		return fmt.Errorf("%w: %s must be a directory of ours with mode 0700", ErrUnsafe, dir)
	}
	return nil
}

// keyFile is the path of one key file.
type keyFile string

// load reads a private PKCS#8 PEM Ed25519 key.
func load(path keyFile) (ed25519.PrivateKey, error) {
	info, err := os.Lstat(string(path))
	if err != nil {
		return nil, err
	}
	if !(info.Mode().IsRegular() && private(info)) {
		return nil, fmt.Errorf("%w: %s must be a file of ours with mode 0600", ErrUnsafe, path)
	}
	data, err := os.ReadFile(string(path))
	if err != nil {
		return nil, err
	}
	return parse(data, path)
}

// private: ours, and no one else can use it.
func private(info fs.FileInfo) bool {
	return info.Mode().Perm()&0o077 == 0 && ownedByUs(info)
}

func parse(data []byte, path keyFile) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("personkey: %s is not a PEM private key", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("personkey: %s is not an Ed25519 key", path)
	}
	return ed, nil
}

// create writes key to a new file at path, 0600, never over another.
func create(path keyFile, key ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(string(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	return errors.Join(werr, f.Sync(), f.Close())
}
