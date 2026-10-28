package firstcontact

import (
	"crypto/ed25519"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// tlsRole is what one end offers and accepts: its ALPNs, and a check on the
// negotiated protocol and the peer's fingerprint beyond the shared shape.
type tlsRole struct {
	server    bool
	protocols []string
	accept    func(protocol, fingerprint string) error
}

// selfSignedTLSConfig is first contact's TLS: TLS 1.3 only, the role's
// ALPNs, no resumption, and each end presenting a self-signed workspace
// certificate. There is deliberately no chain or hostname verification:
// nothing is trusted at first contact. verifySelfSigned checks the shape of
// the peer's certificate instead; the pairing exchange authenticates it, or,
// for a contact connection, the peer's pinned fingerprint does.
func selfSignedTLSConfig(own tls.Certificate, self string, clock func() time.Time, role tlsRole) *tls.Config {
	cfg := &tls.Config{
		MinVersion:             tls.VersionTLS13,
		MaxVersion:             tls.VersionTLS13,
		NextProtos:             role.protocols,
		SessionTicketsDisabled: true,
		Time:                   clock,
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifySelfSigned(cs, self, clock(), role)
		},
	}
	if role.server {
		cfg.Certificates = []tls.Certificate{own}
		cfg.ClientAuth = tls.RequireAnyClientCert
		return cfg
	}
	cfg.InsecureSkipVerify = true // self-signed first contact; see above
	cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &own, nil }
	return cfg
}

// verifySelfSigned accepts one of the role's protocols over TLS 1.3 and
// exactly one current Ed25519 certificate that isn't ours, then the role's
// own check.
func verifySelfSigned(cs tls.ConnectionState, self string, now time.Time, role tlsRole) error {
	if cs.Version != tls.VersionTLS13 || !slices.Contains(role.protocols, cs.NegotiatedProtocol) {
		return fmt.Errorf("%w: want TLS 1.3 with ALPN %q", ErrFirstContact, role.protocols)
	}
	if len(cs.PeerCertificates) != 1 {
		return fmt.Errorf("%w: peer presented %d certificates, want 1", ErrFirstContact, len(cs.PeerCertificates))
	}
	leaf := cs.PeerCertificates[0]
	if err := verifyLeaf(leaf, self, now); err != nil {
		return err
	}
	if role.accept == nil {
		return nil
	}
	return role.accept(cs.NegotiatedProtocol, peer.SPKIFingerprint(leaf))
}

func verifyLeaf(leaf *x509.Certificate, self string, now time.Time) error {
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return fmt.Errorf("%w: peer certificate not valid now", ErrFirstContact)
	}
	if _, ok := leaf.PublicKey.(ed25519.PublicKey); !ok {
		return fmt.Errorf("%w: peer key is not Ed25519", ErrFirstContact)
	}
	fp := peer.SPKIFingerprint(leaf)
	if err := peer.ValidIdentityKey(fp); err != nil {
		return fmt.Errorf("%w: %w", ErrFirstContact, err)
	}
	if fp == self {
		return peer.ErrSelfPairing
	}
	return nil
}

// pairingRole offers the pairing exchange only.
func pairingRole(server bool) tlsRole {
	return tlsRole{server: server, protocols: []string{ALPN}}
}

// serverRole offers contact connections too when pins are given, and
// accepts one only from a pinned identity.
func serverRole(opts ServerOptions) tlsRole {
	if opts.ContactPins == nil {
		return pairingRole(true)
	}
	return tlsRole{server: true, protocols: []string{ALPN, ContactALPN}, accept: func(protocol, fingerprint string) error {
		if protocol == ContactALPN && !opts.ContactPins(fingerprint) {
			return fmt.Errorf("%w: %w", ErrFirstContact, peer.ErrPeerMismatch)
		}
		return nil
	}}
}

// contactRole is a contact connection to the one identity it pins.
func contactRole(pin string) tlsRole {
	return tlsRole{protocols: []string{ContactALPN}, accept: func(_, fingerprint string) error {
		if subtle.ConstantTimeCompare([]byte(fingerprint), []byte(pin)) != 1 {
			return fmt.Errorf("%w: %w", ErrFirstContact, peer.ErrPeerMismatch)
		}
		return nil
	}}
}
