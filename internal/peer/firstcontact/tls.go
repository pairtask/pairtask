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

// pairingRole offers the pairing exchange only, over the given pairing
// ALPNs in preference order, or by default PairingProtocols().
func pairingRole(server bool, protocols []string) tlsRole {
	if protocols == nil {
		protocols = PairingProtocols()
	}
	return tlsRole{server: server, protocols: slices.Clone(protocols)}
}

// serverRole offers contact connections and message sessions too when their
// pins are given, and accepts each only from an identity its pins admit.
// The pairing ALPNs come first, in preference order: crypto/tls picks the
// first of the server's protocols the client offers, so the server's order
// decides the exchange's mode (#276).
func serverRole(opts ServerOptions) tlsRole {
	role := pairingRole(true, opts.PairingProtocols)
	pins := map[string]func(string) bool{}
	var pinned []string
	for protocol, admit := range map[string]func(string) bool{ContactALPN: opts.ContactPins, MessageALPN: opts.MessagePins} {
		if admit != nil {
			pins[protocol] = admit
			pinned = append(pinned, protocol)
		}
	}
	if len(pins) == 0 {
		return role
	}
	slices.Sort(pinned)
	return tlsRole{server: true, protocols: append(role.protocols, pinned...), accept: func(protocol, fingerprint string) error {
		if admit, pinned := pins[protocol]; pinned && !admit(fingerprint) {
			return fmt.Errorf("%w: %w", ErrFirstContact, peer.ErrPeerMismatch)
		}
		return nil
	}}
}

// contactRole is a contact connection to the one identity it pins.
func contactRole(pin string) tlsRole { return pinned{ContactALPN, pin}.role() }

// pinned is one protocol offered to the one identity it pins.
type pinned struct{ protocol, fingerprint string }

// role offers the protocol and accepts only the pinned identity.
func (p pinned) role() tlsRole {
	pin := p.fingerprint
	return tlsRole{protocols: []string{p.protocol}, accept: func(_, fingerprint string) error {
		if subtle.ConstantTimeCompare([]byte(fingerprint), []byte(pin)) != 1 {
			return fmt.Errorf("%w: %w", ErrFirstContact, peer.ErrPeerMismatch)
		}
		return nil
	}}
}
