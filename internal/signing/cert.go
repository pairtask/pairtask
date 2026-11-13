package signing

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"
)

// Kind is what a certified key signs for.
type Kind string

const (
	// KindBroker signs agents' messages on their behalf, attested by its
	// workspace (phase 1a: agents share one Unix user, so no agent key of
	// their own would be private).
	KindBroker Kind = "broker"
	// KindAgent is one agent's own key (phase 1b, per-agent Unix users).
	KindAgent Kind = "agent"
	// KindDevice is one place a person acts from: the operator chat, a
	// Desktop extension.
	KindDevice Kind = "device"
)

const certDomain = "pairtask-cert-v1"

// Cert binds a key to what it signs for, signed by its issuer: the
// workspace key for brokers and agents, the person key for devices.
type Cert struct {
	Kind      Kind
	Subject   string // agent ID for an agent, a label for a broker or device
	Key       ed25519.PublicKey
	Workspace string // key ID of the workspace this key belongs to
	Issuer    string // key ID of the signing key
	NotBefore time.Time
	NotAfter  time.Time
	Sig       []byte
}

var (
	// ErrExpired: a certificate is outside its validity.
	ErrExpired = errors.New("signing: certificate not valid now")
	// ErrBadSignature: a signature doesn't verify.
	ErrBadSignature = errors.New("signing: bad signature")
)

// Issue certifies c with issuer; it fills Issuer and Sig.
func Issue(issuer ed25519.PrivateKey, c Cert) Cert {
	c.Issuer = KeyID(issuer.Public().(ed25519.PublicKey))
	c.Sig = ed25519.Sign(issuer, c.tbs())
	return c
}

// Check verifies c's signature by issuer and its validity at now.
func (c Cert) Check(issuer ed25519.PublicKey, now time.Time) error {
	if KeyID(issuer) != c.Issuer || !ed25519.Verify(issuer, c.tbs(), c.Sig) {
		return fmt.Errorf("%w: certificate for %s %q", ErrBadSignature, c.Kind, c.Subject)
	}
	if now.Before(c.NotBefore) || !now.Before(c.NotAfter) {
		return fmt.Errorf("%w: %s %q", ErrExpired, c.Kind, c.Subject)
	}
	return nil
}

// sized: the key and signature have their Ed25519 sizes.
func (c Cert) sized() bool {
	return len(c.Key) == ed25519.PublicKeySize && len(c.Sig) == ed25519.SignatureSize
}

// KeyID is the certified key's ID.
func (c Cert) KeyID() string { return KeyID(c.Key) }

// tbs is what the issuer signs.
func (c Cert) tbs() []byte {
	w := writer{}
	w.str(certDomain)
	w.str(string(c.Kind))
	w.str(c.Subject)
	w.bytes(c.Key)
	w.str(c.Workspace)
	w.str(c.Issuer)
	w.u64(uint64(c.NotBefore.UnixMilli()))
	w.u64(uint64(c.NotAfter.UnixMilli()))
	return w.b
}

// Marshal encodes c with its signature.
func (c Cert) Marshal() []byte {
	w := writer{}
	w.bytes(c.tbs())
	w.bytes(c.Sig)
	return w.b
}

// ParseCert decodes a Marshal'd certificate; it checks shape, not trust.
func ParseCert(b []byte) (Cert, error) {
	outer := reader{b: b}
	tbs, sig := outer.bytes(), outer.bytes()
	if err := outer.done(); err != nil {
		return Cert{}, err
	}
	r := reader{b: tbs}
	if r.str() != certDomain {
		return Cert{}, errMalformed
	}
	c := Cert{Kind: Kind(r.str()), Subject: r.str(), Key: ed25519.PublicKey(r.bytes()), Workspace: r.str(), Issuer: r.str(),
		NotBefore: time.UnixMilli(int64(r.u64())), NotAfter: time.UnixMilli(int64(r.u64())), Sig: sig}
	if err := r.done(); err != nil {
		return Cert{}, err
	}
	if !c.sized() {
		return Cert{}, errMalformed
	}
	return c, nil
}
