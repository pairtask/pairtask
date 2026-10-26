package pairing_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	p "github.com/pairtask/pairtask/spike/pairing"
)

func acceptanceCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spec := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, spec, spec, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type acceptanceLink struct {
	client, server *p.Endpoint
	a, b           p.Binding
}

func acceptanceTLS(t *testing.T, client, server tls.Certificate) acceptanceLink {
	t.Helper()
	left, right := net.Pipe()
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	_ = left.SetDeadline(time.Now().Add(5 * time.Second))
	_ = right.SetDeadline(time.Now().Add(5 * time.Second))
	c, s := p.Client(left, client), p.Server(right, server)
	if _, err := c.Binding([]byte("invite-17")); err == nil {
		t.Fatal("binding before handshake accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Handshake(ctx) }()
	if err := c.Handshake(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	a, err := c.Binding([]byte("invite-17"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Binding([]byte("invite-17"))
	if err != nil {
		t.Fatal(err)
	}
	return acceptanceLink{c, s, a, b}
}

type acceptanceResult struct {
	result p.Result
	err    error
}

func acceptanceExchange(t *testing.T, link acceptanceLink, codeA, codeB string) (acceptanceResult, acceptanceResult) {
	t.Helper()
	done := make(chan acceptanceResult, 1)
	go func() {
		r, err := p.Invite(link.server.Frames(), link.b, []byte(codeB))
		done <- acceptanceResult{r, err}
		_ = link.server.Conn.NetConn().Close()
	}()
	a, err := p.Join(link.client.Frames(), link.a, []byte(codeA))
	_ = link.client.Conn.NetConn().Close()
	return acceptanceResult{a, err}, <-done
}

func TestTransportAcceptanceRealTLSBindsBothCertificates(t *testing.T) {
	client, server := acceptanceCertificate(t), acceptanceCertificate(t)
	link := acceptanceTLS(t, client, server)
	if !bytes.Equal(link.a.EKM, link.b.EKM) || len(link.a.EKM) != 32 {
		t.Fatal("TLS endpoints disagree on exporter")
	}
	if link.a.JoinerFP != sha256.Sum256(client.Certificate[0]) || link.a.InviterFP != sha256.Sum256(server.Certificate[0]) || link.a.JoinerFP != link.b.JoinerFP || link.a.InviterFP != link.b.InviterFP {
		t.Fatal("binding does not name actually presented certificates")
	}
	a, b := acceptanceExchange(t, link, "047291", "047291")
	if a.err != nil || b.err != nil {
		t.Fatalf("pairing failed: join=%v invite=%v", a.err, b.err)
	}
	if !a.result.InviterConfirmed || a.result.Peer != link.a.InviterFP || b.result.Peer != link.b.JoinerFP || !bytes.Equal(a.result.TranscriptID, b.result.TranscriptID) {
		t.Fatal("confirmed pairing result disagrees")
	}
	another := acceptanceTLS(t, client, server)
	if bytes.Equal(link.a.EKM, another.a.EKM) {
		t.Fatal("new TLS connection reused exporter")
	}
}

func TestTransportAcceptanceContextChangesFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*acceptanceLink)
		code   string
	}{
		{"wrong-code", func(*acceptanceLink) {}, "047292"},
		{"invitation", func(l *acceptanceLink) { l.b.Invitation = []byte("different") }, "047291"},
		{"exporter", func(l *acceptanceLink) { l.b.EKM = bytes.Clone(l.b.EKM); l.b.EKM[0] ^= 1 }, "047291"},
		{"inviter-key", func(l *acceptanceLink) { l.b.InviterFP[0] ^= 1 }, "047291"},
		{"joiner-key", func(l *acceptanceLink) { l.b.JoinerFP[0] ^= 1 }, "047291"},
		{"roles", func(l *acceptanceLink) { l.b.JoinerFP, l.b.InviterFP = l.b.InviterFP, l.b.JoinerFP }, "047291"},
	}
	client, server := acceptanceCertificate(t), acceptanceCertificate(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			link := acceptanceTLS(t, client, server)
			tc.mutate(&link)
			a, b := acceptanceExchange(t, link, "047291", tc.code)
			if a.err == nil || b.err == nil || len(a.result.TranscriptID) != 0 || len(b.result.TranscriptID) != 0 {
				t.Fatalf("tampered connection confirmed: %v / %v", a.err, b.err)
			}
		})
	}
}

func TestTransportAcceptanceTwoSessionRelayMITMFails(t *testing.T) {
	left := acceptanceTLS(t, acceptanceCertificate(t), acceptanceCertificate(t))
	right := acceptanceTLS(t, acceptanceCertificate(t), acceptanceCertificate(t))
	if bytes.Equal(left.a.EKM, right.b.EKM) {
		t.Fatal("test requires distinct TLS sessions")
	}
	proxy := make(chan error, 1)
	go func() {
		defer left.server.Conn.NetConn().Close()
		defer right.client.Conn.NetConn().Close()
		for _, step := range [][2]p.Frames{{left.server.Frames(), right.client.Frames()}, {right.client.Frames(), left.server.Frames()}, {left.server.Frames(), right.client.Frames()}, {right.client.Frames(), left.server.Frames()}} {
			frame, err := step[0].Recv()
			if err != nil {
				proxy <- err
				return
			}
			if err := step[1].Send(frame); err != nil {
				proxy <- err
				return
			}
		}
		proxy <- nil
	}()
	done := make(chan acceptanceResult, 1)
	go func() {
		r, err := p.Invite(right.server.Frames(), right.b, []byte("047291"))
		done <- acceptanceResult{r, err}
		_ = right.server.Conn.NetConn().Close()
	}()
	a, err := p.Join(left.client.Frames(), left.a, []byte("047291"))
	_ = left.client.Conn.NetConn().Close()
	b := <-done
	if err == nil || b.err == nil || len(a.TranscriptID) != 0 || len(b.result.TranscriptID) != 0 {
		t.Fatal("relay authenticated across unrelated TLS connections")
	}
	<-proxy
}

type acceptanceScript struct {
	incoming [][]byte
	sent     [][]byte
	failSend int
}

func (s *acceptanceScript) Send(b []byte) error {
	s.sent = append(s.sent, bytes.Clone(b))
	if len(s.sent) == s.failSend {
		return io.ErrClosedPipe
	}
	return nil
}
func (s *acceptanceScript) Recv() ([]byte, error) {
	if len(s.incoming) == 0 {
		return nil, io.EOF
	}
	v := s.incoming[0]
	s.incoming = s.incoming[1:]
	return v, nil
}

type acceptanceDropFinal struct {
	p.Frames
	sent int
}

func (f *acceptanceDropFinal) Send(b []byte) error {
	f.sent++
	if f.sent == 2 {
		return io.ErrClosedPipe
	}
	return f.Frames.Send(b)
}

func TestTransportAcceptanceLostFinalAckIsNotSuccess(t *testing.T) {
	link := acceptanceTLS(t, acceptanceCertificate(t), acceptanceCertificate(t))
	done := make(chan acceptanceResult, 1)
	go func() {
		r, err := p.Invite(&acceptanceDropFinal{Frames: link.server.Frames()}, link.b, []byte("047291"))
		done <- acceptanceResult{r, err}
		_ = link.server.Conn.NetConn().Close()
	}()
	a, err := p.Join(link.client.Frames(), link.a, []byte("047291"))
	_ = link.client.Conn.NetConn().Close()
	b := <-done
	if !errors.Is(err, p.ErrOutcomeUnknown) || a.InviterConfirmed || len(a.TranscriptID) == 0 {
		t.Fatalf("joiner lost outcome distinction: %+v %v", a, err)
	}
	if b.err == nil || len(b.result.TranscriptID) == 0 {
		t.Fatal("inviter lost verified identity on reply failure")
	}
}

func TestTransportAcceptanceFrameBounds(t *testing.T) {
	for _, wire := range [][]byte{nil, {0, 0, 0}, {0, 0, 0, 4, 1}, binary.BigEndian.AppendUint32(nil, 1025)} {
		f := p.StreamFrames{RW: bytes.NewBuffer(wire)}
		if _, err := f.Recv(); err == nil {
			t.Fatal("truncated/oversized frame accepted")
		}
	}
	var buf bytes.Buffer
	f := p.StreamFrames{RW: &buf}
	if err := f.Send(make([]byte, 1025)); err == nil || buf.Len() != 0 {
		t.Fatal("oversized send wrote data")
	}
	if err := f.Send([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	v, err := f.Recv()
	if err != nil || string(v) != "ok" {
		t.Fatal("valid frame roundtrip failed")
	}
}
