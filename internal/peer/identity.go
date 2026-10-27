package peer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Workspace identity (#178). Each workspace has one long-term Ed25519 key.
// Its pairtask SPKI fingerprint, "sha256:" and the lowercase hex SHA-256 of
// the key's SubjectPublicKeyInfo DER, names the workspace to its peers.
// Certificates are self-signed from that key and may be renewed freely: peers
// pin the fingerprint, never a certificate hash. (RFC 7469 pins encode the
// same digest in base64; this is not that format.)
//
// The key is never replaced: once the store records the fingerprint, a lost,
// corrupt or different key fails closed rather than quietly becoming a new
// workspace. Rotation is out of scope; a new key needs a new pairing.

var (
	// ErrIdentityMissing: the store knows this workspace's identity but its key
	// file is gone.
	ErrIdentityMissing = errors.New("peer: identity key missing for an initialized workspace")
	// ErrIdentityCorrupt: the key file isn't one Ed25519 PKCS#8 PEM key.
	ErrIdentityCorrupt = errors.New("peer: identity key unreadable")
	// ErrIdentityUnsafe: the key file or its directory is a symlink, not a
	// regular file or directory, not ours, or readable by others.
	ErrIdentityUnsafe = errors.New("peer: identity key path unsafe")
	// ErrIdentityMismatch: the key differs from the identity the store knows.
	ErrIdentityMismatch = errors.New("peer: identity key does not match this workspace")
	// ErrIdentityUnavailable: there is nowhere to keep a key, e.g. an
	// in-memory database.
	ErrIdentityUnavailable = errors.New("peer: no identity storage")
	// ErrInvalidIdentityKey: not a pairtask SPKI fingerprint.
	ErrInvalidIdentityKey = errors.New("peer: invalid identity key")
)

// IdentityMarker durably records which identity this workspace initialized.
type IdentityMarker interface {
	IdentityFingerprint(ctx context.Context) (fp string, ok bool, err error)
	// InitIdentity records fp once; the same fp again is a no-op and a
	// different one is ErrIdentityMismatch.
	InitIdentity(ctx context.Context, fp string) error
}

// WorkspaceIdentity is this workspace's key. It never formats or encodes the key.
type WorkspaceIdentity struct {
	key         ed25519.PrivateKey
	fingerprint string
}

func newIdentity(key ed25519.PrivateKey) (*WorkspaceIdentity, error) {
	spki, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return nil, err
	}
	return &WorkspaceIdentity{key: key, fingerprint: spkiFingerprint(spki)}, nil
}

// Fingerprint is the workspace's pairtask SPKI fingerprint.
func (i *WorkspaceIdentity) Fingerprint() string { return i.fingerprint }

func (i *WorkspaceIdentity) String() string { return "peer.WorkspaceIdentity{" + i.fingerprint + "}" }

func (i *WorkspaceIdentity) GoString() string { return i.String() }

// Certificate is a self-signed certificate for the key, valid from a minute
// before now for validity. Each one differs; all carry the same fingerprint.
func (i *WorkspaceIdentity) Certificate(name string, now time.Time, validity time.Duration) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              []string{name},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, i.key.Public(), i.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: i.key, Leaf: leaf}, nil
}

func spkiFingerprint(spki []byte) string {
	sum := sha256.Sum256(spki)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// SPKIFingerprint is a certificate's pairtask SPKI fingerprint, or "" when it
// carries no public key.
func SPKIFingerprint(cert *x509.Certificate) string {
	if cert == nil || len(cert.RawSubjectPublicKeyInfo) == 0 {
		return ""
	}
	return spkiFingerprint(cert.RawSubjectPublicKeyInfo)
}

// CertificateHasIdentity reports whether a certificate carries the key with
// fingerprint fp. It only extracts and compares: it is no proof that the
// sender holds the key, nor trust in the certificate. That needs the
// transport binding of a later slice.
func CertificateHasIdentity(cert *x509.Certificate, fp string) bool {
	got := SPKIFingerprint(cert)
	if got == "" || fp == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(fp)) == 1
}

// ValidIdentityKey checks the canonical fingerprint form.
func ValidIdentityKey(key string) error {
	digest, ok := strings.CutPrefix(key, "sha256:")
	if !ok || len(digest) != 2*sha256.Size {
		return fmt.Errorf("%w: want sha256: and 64 lowercase hex digits", ErrInvalidIdentityKey)
	}
	if strings.ContainsFunc(digest, notLowerHex) {
		return fmt.Errorf("%w: want sha256: and 64 lowercase hex digits", ErrInvalidIdentityKey)
	}
	return nil
}

func notLowerHex(r rune) bool {
	return !strings.ContainsRune("0123456789abcdef", r)
}
