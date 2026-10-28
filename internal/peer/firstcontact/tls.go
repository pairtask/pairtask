package firstcontact

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

// selfSignedTLSConfig is first contact's TLS: TLS 1.3 only, this protocol's
// ALPN, no resumption, and each end presenting a self-signed workspace
// certificate. There is deliberately no chain or hostname verification:
// nothing is trusted at first contact. verifySelfSigned checks the shape of
// the peer's certificate instead, and the pairing exchange authenticates it.
func selfSignedTLSConfig(own tls.Certificate, self string, clock func() time.Time, server bool) *tls.Config {
	cfg := &tls.Config{
		MinVersion:             tls.VersionTLS13,
		MaxVersion:             tls.VersionTLS13,
		NextProtos:             []string{ALPN},
		SessionTicketsDisabled: true,
		Time:                   clock,
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifySelfSigned(cs, self, clock())
		},
	}
	if server {
		cfg.Certificates = []tls.Certificate{own}
		cfg.ClientAuth = tls.RequireAnyClientCert
		return cfg
	}
	cfg.InsecureSkipVerify = true // self-signed first contact; see above
	cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &own, nil }
	return cfg
}

// verifySelfSigned accepts this protocol over TLS 1.3 and exactly one
// current Ed25519 certificate that isn't ours.
func verifySelfSigned(cs tls.ConnectionState, self string, now time.Time) error {
	if cs.Version != tls.VersionTLS13 || cs.NegotiatedProtocol != ALPN {
		return fmt.Errorf("%w: want TLS 1.3 with ALPN %q", ErrFirstContact, ALPN)
	}
	if len(cs.PeerCertificates) != 1 {
		return fmt.Errorf("%w: peer presented %d certificates, want 1", ErrFirstContact, len(cs.PeerCertificates))
	}
	return verifyLeaf(cs.PeerCertificates[0], self, now)
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
