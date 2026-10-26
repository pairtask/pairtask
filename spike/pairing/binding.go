// Package pairing is an evaluation spike for #178's short-code pairing: CPace
// (the-sarge/cpace v0.1.3, draft-irtf-cfrg-cpace-21) run inside a first-contact
// TLS 1.3 connection whose certificates aren't trusted yet, and bound to that
// connection and to both presented certificates. It is not wired into the
// broker and decides nothing about adoption.
package pairing

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"
)

// Role is which end of the pairing this side is.
type Role int

const (
	// Joiner typed the code: the TLS client and the CPace initiator.
	Joiner Role = iota
	// Inviter showed the code: the TLS server and the CPace responder.
	Inviter
)

// exporterLabel is RFC 9266's tls-exporter channel binding: empty context, 32
// bytes. It is binding data, not a key.
const exporterLabel = "EXPORTER-Channel-Binding"

// version names this pairing profile; it is bound into every exchange.
const version = "pairtask-pairing-v1"

// Binding is what one end sees on its own first-contact connection.
type Binding struct {
	Invitation          []byte   // non-secret invitation ID, shown with the code
	InviterFP, JoinerFP [32]byte // SHA-256 of the presented leaf certificates
	EKM                 []byte   // RFC 9266 channel binding of this connection
}

// Endpoint is one end of a first-contact connection. It configures TLS 1.3
// itself and records the certificate it actually presented, so a binding can
// only name the leaf that went over the wire.
type Endpoint struct {
	Conn *tls.Conn
	role Role

	mu        sync.Mutex
	presented []byte // DER of the leaf this end sent
}

// Client is the joiner's end over raw. Certificates aren't verified here:
// at first contact nothing is trusted yet, and the pairing exchange is what
// authenticates the connection. Only a single parsable leaf is accepted.
func Client(raw net.Conn, own tls.Certificate) *Endpoint {
	e := &Endpoint{role: Joiner}
	e.Conn = tls.Client(raw, &tls.Config{
		MinVersion:            tls.VersionTLS13,
		InsecureSkipVerify:    true, // authenticated by the pairing exchange, see above
		VerifyPeerCertificate: singleLeaf,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return e.present(own)
		},
	})
	return e
}

// Server is the inviter's end over raw; it requires the joiner's certificate.
func Server(raw net.Conn, own tls.Certificate) *Endpoint {
	e := &Endpoint{role: Inviter}
	e.Conn = tls.Server(raw, &tls.Config{
		MinVersion:             tls.VersionTLS13,
		SessionTicketsDisabled: true, // one fresh pairing connection, never resumed
		ClientAuth:             tls.RequireAnyClientCert,
		VerifyPeerCertificate: singleLeaf,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return e.present(own)
		},
	})
	return e
}

func (e *Endpoint) present(own tls.Certificate) (*tls.Certificate, error) {
	if len(own.Certificate) == 0 {
		return nil, errors.New("pairing: no certificate to present")
	}
	e.mu.Lock()
	e.presented = append([]byte(nil), own.Certificate[0]...)
	e.mu.Unlock()
	return &own, nil
}

// singleLeaf accepts exactly one parsable certificate from the peer.
func singleLeaf(raw [][]byte, _ [][]*x509.Certificate) error {
	if len(raw) != 1 {
		return fmt.Errorf("pairing: peer presented %d certificates, want 1", len(raw))
	}
	_, err := x509.ParseCertificate(raw[0])
	return err
}

// Handshake completes the TLS handshake.
func (e *Endpoint) Handshake(ctx context.Context) error { return e.Conn.HandshakeContext(ctx) }

// Binding reads the completed connection: the leaf this end presented, the one
// the peer presented, and the exporter.
func (e *Endpoint) Binding(invitation []byte) (Binding, error) {
	cs := e.Conn.ConnectionState()
	if !cs.HandshakeComplete || cs.Version != tls.VersionTLS13 {
		return Binding{}, errors.New("pairing: needs a completed TLS 1.3 handshake")
	}
	e.mu.Lock()
	presented := e.presented
	e.mu.Unlock()
	if len(cs.PeerCertificates) != 1 || presented == nil {
		return Binding{}, errors.New("pairing: both ends must have presented one certificate")
	}
	ekm, err := cs.ExportKeyingMaterial(exporterLabel, nil, 32)
	if err != nil {
		return Binding{}, fmt.Errorf("pairing: channel binding: %w", err)
	}
	self, peer := sha256.Sum256(presented), sha256.Sum256(cs.PeerCertificates[0].Raw)
	b := Binding{Invitation: append([]byte(nil), invitation...), EKM: ekm, InviterFP: self, JoinerFP: peer}
	if e.role == Joiner {
		b.InviterFP, b.JoinerFP = peer, self
	}
	return b, nil
}

// Frames returns length-prefixed framing over this connection.
func (e *Endpoint) Frames() StreamFrames { return StreamFrames{RW: e.Conn} }

// identity is the party identity bound into CPace's channel identifier.
func identity(role Role, fp [32]byte) []byte {
	name := map[Role]string{Joiner: "pairtask-joiner:", Inviter: "pairtask-inviter:"}[role]
	return []byte(name + hex.EncodeToString(fp[:]))
}

// context is the length-prefixed profile version and invitation.
func (b Binding) context() []byte {
	var out []byte
	for _, field := range [][]byte{[]byte(version), b.Invitation} {
		out = binary.BigEndian.AppendUint32(out, uint32(len(field)))
		out = append(out, field...)
	}
	return out
}
