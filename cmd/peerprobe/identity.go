package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pairtask/pairtask/internal/peer"
)

// identityPaths is where a side keeps its self-signed identity.
type identityPaths struct{ dir, name string }

func (p identityPaths) cert() string { return filepath.Join(p.dir, p.name+".crt") }
func (p identityPaths) key() string  { return filepath.Join(p.dir, p.name+".key") }

// loadIdentity loads the identity, or creates one when neither file exists.
// A half-present or unreadable identity is an error, never silently
// replaced, so a restart can't mask a lost key.
func loadIdentity(p identityPaths) (tls.Certificate, error) {
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	if exists(p.cert()) || exists(p.key()) {
		cert, err := tls.LoadX509KeyPair(p.cert(), p.key())
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("existing identity in %s can't be loaded: %w", p.dir, err)
		}
		return cert, nil
	}
	return createIdentity(p)
}

func createIdentity(p identityPaths) (tls.Certificate, error) {
	cert, err := peer.SelfSigned(p.name)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM, keyPEM, err := peer.EncodePEM(cert)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writeNew(p.key(), keyPEM); err != nil {
		return tls.Certificate{}, err
	}
	if err := writeNew(p.cert(), certPEM); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(p.cert(), p.key())
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// writeNew creates a private file that must not exist yet.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// removeStaleSocket unlinks a leftover Unix socket at path, and refuses to
// touch anything else there: a regular file, directory or link stays.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode()&os.ModeSocket == 0:
		return fmt.Errorf("%s exists and is not a socket; refusing to replace it", path)
	}
	return os.Remove(path)
}
