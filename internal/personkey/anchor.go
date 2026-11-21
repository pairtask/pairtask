package personkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
)

// The identity anchor (#392): the person's genesis statement, signed by the
// person key and naming the recovery key R. It fixes the person's lineage,
// so it is made once, kept beside the keys and never made again: a second
// anchor would be a second person. R's private key is never stored; the
// operator gets it once as a recovery code to write down.

const (
	anchorFile    = "anchor.bin"
	confirmedFile = "recovery.confirmed"
	recoveryTag   = "PTR1"
	recoveryCheck = "pairtask-recovery-v1"
	groupSize     = 5
)

var (
	ErrNoAnchor      = errors.New("personkey: no identity anchor yet")
	ErrAnchorExists  = errors.New("personkey: an identity anchor exists already")
	ErrAnchorInvalid = errors.New("personkey: the identity anchor is damaged or isn't this person's")
	ErrRecoveryCode  = errors.New("personkey: not a valid recovery code")
)

var recoveryText = base32.StdEncoding.WithPadding(base32.NoPadding)

// EncodeRecovery is the recovery code for R's 32-byte seed: "PTR1-" and
// the base32 of the seed and a 2-byte checksum, in groups of five.
func EncodeRecovery(seed []byte) (string, error) {
	if len(seed) != ed25519.SeedSize {
		return "", ErrRecoveryCode
	}
	text := recoveryText.EncodeToString(append(append([]byte(nil), seed...), checksum(seed)...))
	groups := []string{recoveryTag}
	for len(text) > 0 {
		n := min(groupSize, len(text))
		groups = append(groups, text[:n])
		text = text[n:]
	}
	return strings.Join(groups, "-"), nil
}

// DecodeRecovery is R's private key from a recovery code, read without
// regard to case, spaces, dashes or line breaks.
func DecodeRecovery(code string) (ed25519.PrivateKey, error) {
	text := strings.ToUpper(strings.NewReplacer("-", "", " ", "", "\n", "", "\r", "", "\t", "").Replace(code))
	body, ok := strings.CutPrefix(text, recoveryTag)
	if !ok {
		return nil, ErrRecoveryCode
	}
	payload, err := canonicalPayload(body)
	if err != nil {
		return nil, err
	}
	seed := payload[:ed25519.SeedSize]
	if string(checksum(seed)) != string(payload[ed25519.SeedSize:]) {
		return nil, ErrRecoveryCode
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// canonicalPayload decodes body only if it is exactly the 55 characters
// that encode a 34-byte payload, with no stray trailing bits: base32 can
// otherwise ignore a mistyped last character.
func canonicalPayload(body string) ([]byte, error) {
	const textLen = 55 // base32 of 34 bytes, unpadded
	if len(body) != textLen {
		return nil, ErrRecoveryCode
	}
	payload, err := recoveryText.DecodeString(body)
	if err != nil || len(payload) != ed25519.SeedSize+2 || recoveryText.EncodeToString(payload) != body {
		return nil, ErrRecoveryCode
	}
	return payload, nil
}

func checksum(seed []byte) []byte {
	sum := sha256.Sum256(append([]byte(recoveryCheck), seed...))
	return sum[:2]
}

// CreateAnchor makes the person's identity anchor in dir, beside existing
// keys, with InitAt at. withRecovery makes a fresh R and returns its
// recovery code (shown once, never stored); without it the anchor has no
// R. Only one anchor is ever made: if one exists, or another call wins a
// race, it returns ErrAnchorExists and no code.
func CreateAnchor(dir string, withRecovery bool, at time.Time) ([]byte, string, error) {
	keys, err := Load(dir)
	if err != nil {
		return nil, "", err
	}
	if _, err := os.Lstat(filepath.Join(dir, anchorFile)); !errors.Is(err, fs.ErrNotExist) {
		return nil, "", ErrAnchorExists
	}
	a := identity.Anchor{Person: keys.PersonPublic(), InitAt: at}
	code := ""
	if withRecovery {
		r, c, err := newRecovery()
		if err != nil {
			return nil, "", err
		}
		a.Recovery, code = r, c
	}
	body := a.Signed(keys.person).Marshal()
	if err := writeOnce(dir, anchorFile, body); err != nil {
		return nil, "", err
	}
	return body, code, nil
}

// newRecovery is a fresh recovery public key and its code.
func newRecovery() (ed25519.PublicKey, string, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, "", err
	}
	code, err := EncodeRecovery(seed)
	return ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), code, err
}

// writeOnce publishes body at dir/name, 0600, only if nothing is there:
// written whole to a temporary file first, then linked into place, so no
// reader ever sees part of it and an existing file is never replaced.
func writeOnce(dir, name string, body []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := writeSynced(tmp, body); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), filepath.Join(dir, name)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrAnchorExists
		}
		return err
	}
	return syncDir(dir)
}

func writeSynced(f *os.File, body []byte) error {
	_, werr := f.Write(body)
	serr := f.Sync()
	cerr := f.Close()
	return errors.Join(werr, serr, cerr)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// Anchor is the stored identity anchor: checked to be private, intact,
// signed and this person's.
func (k *Keys) Anchor() ([]byte, error) {
	body, err := readPrivate(filepath.Join(k.dir, anchorFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoAnchor
	}
	if err != nil {
		return nil, err
	}
	a, err := identity.ParseAnchor(body)
	if err != nil || a.Check() != nil || !a.Person.Equal(k.PersonPublic()) {
		return nil, ErrAnchorInvalid
	}
	return body, nil
}

// readPrivate reads a regular file of ours that no one else can use.
func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !(info.Mode().IsRegular() && private(info)) {
		return nil, fmt.Errorf("%w: %s must be a file of ours with mode 0600", ErrUnsafe, path)
	}
	return os.ReadFile(path)
}

// ConfirmRecovery records that the operator typed back part of the code.
func ConfirmRecovery(dir string) error {
	err := writeOnce(dir, confirmedFile, []byte("confirmed\n"))
	if errors.Is(err, ErrAnchorExists) {
		return nil // confirmed before
	}
	return err
}

// RecoveryConfirmed: ConfirmRecovery was recorded.
func RecoveryConfirmed(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, confirmedFile))
	return err == nil
}
