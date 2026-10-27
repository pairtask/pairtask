package pairing

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

func selfSigned(t *testing.T, name string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// connect hands back both ends of one first-contact connection, handshaken.
func connect(t *testing.T, joiner, inviter tls.Certificate) (*Endpoint, *Endpoint) {
	t.Helper()
	a, b := net.Pipe()
	j, i := Client(a, joiner), Server(b, inviter)
	// Close the pipe, not TLS: a TLS close waits up to 5s to deliver its
	// close_notify, and nobody reads an in-memory pipe after the test.
	t.Cleanup(func() { a.Close(); b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errs := make(chan error, 1)
	go func() { errs <- i.Handshake(ctx) }()
	if err := j.Handshake(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	return j, i
}

func binding(t *testing.T, e *Endpoint, invitation string) Binding {
	t.Helper()
	b, err := e.Binding([]byte(invitation))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type outcome struct {
	join, invite       Result
	joinErr, inviteErr error
}

// bothConfirmed is a pairing both sides completed without error.
func (o outcome) bothConfirmed() bool { return o.joinErr == nil && o.inviteErr == nil }

// joinerConfirmed reports whether the joiner ended up believing it paired.
func (o outcome) joinerConfirmed() bool { return o.joinErr == nil || o.join.InviterConfirmed }

// neitherConfirmed is a pairing that both sides rejected.
func (o outcome) neitherConfirmed() bool { return !o.joinerConfirmed() && o.inviteErr != nil }

// sameBinding reports whether two ends computed the same binding.
func sameBinding(a, b Binding) bool { return a.key() == b.key() }

// bindingKey is a binding's comparable identity: exporter and both fingerprints.
type bindingKey struct {
	ekm                 string
	inviterFP, joinerFP [32]byte
}

func (b Binding) key() bindingKey { return bindingKey{string(b.EKM), b.InviterFP, b.JoinerFP} }

// side is one end of a pairing attempt: its endpoint, binding and code.
type side struct {
	end     *Endpoint
	binding Binding
	code    string
}

func run(joiner, inviter side) outcome {
	var o outcome
	done := make(chan struct{})
	go func() {
		defer close(done)
		o.invite, o.inviteErr = Invite(inviter.end.Frames(), inviter.binding, []byte(inviter.code))
		if o.inviteErr != nil {
			inviter.end.Conn.Close() // a failed inviter hangs up
		}
	}()
	o.join, o.joinErr = Join(joiner.end.Frames(), joiner.binding, []byte(joiner.code))
	if o.joinErr != nil {
		joiner.end.Conn.Close()
	}
	<-done
	return o
}

func TestCorrectCodeConfirmsBothSides(t *testing.T) {
	jc, ic := selfSigned(t, "joiner"), selfSigned(t, "inviter")
	j, i := connect(t, jc, ic)
	o := run(side{j, binding(t, j, "inv-1"), "482913"}, side{i, binding(t, i, "inv-1"), "482913"})
	if !o.bothConfirmed() {
		t.Fatalf("correct code failed: joiner=%v inviter=%v", o.joinErr, o.inviteErr)
	}
	if !o.join.InviterConfirmed {
		t.Fatal("joiner didn't see the inviter's confirmation")
	}
	if o.join.Peer != binding(t, i, "inv-1").InviterFP {
		t.Fatal("the joiner reports the wrong peer")
	}
	if o.invite.Peer != binding(t, j, "inv-1").JoinerFP {
		t.Fatal("the inviter reports the wrong peer")
	}
	if string(o.join.TranscriptID) != string(o.invite.TranscriptID) {
		t.Fatal("the two sides disagree on the transcript")
	}
}

// Each mismatch must leave both sides unconfirmed: nothing to record.
func TestMismatchesConfirmNothing(t *testing.T) {
	cases := map[string]struct{ joinCode, joinInv string }{
		"wrong code":       {"482914", "inv-1"},
		"other invitation": {"482913", "inv-2"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			j, i := connect(t, selfSigned(t, "joiner"), selfSigned(t, "inviter"))
			o := run(side{j, binding(t, j, c.joinInv), c.joinCode}, side{i, binding(t, i, "inv-1"), "482913"})
			if !errors.Is(o.inviteErr, ErrUnconfirmed) {
				t.Fatalf("inviter: got %v, want ErrUnconfirmed", o.inviteErr)
			}
			if o.joinerConfirmed() {
				t.Fatalf("joiner confirmed a mismatched pairing: %+v", o.join)
			}
		})
	}
}

// A man in the middle terminates TLS on both sides with its own certificate
// and relays the pairing frames unchanged, without even guessing the code.
// The two legs differ in exporter and certificates, so neither side may
// confirm.
func TestRelayingManInTheMiddleConfirmsNothing(t *testing.T) {
	jc, ic, mc := selfSigned(t, "joiner"), selfSigned(t, "inviter"), selfSigned(t, "mitm")
	j, mToJoiner := connect(t, jc, mc)
	mToInviter, i := connect(t, mc, ic)
	go relay(mToJoiner.Conn, mToInviter.Conn)
	go relay(mToInviter.Conn, mToJoiner.Conn)
	o := run(side{j, binding(t, j, "inv-1"), "482913"}, side{i, binding(t, i, "inv-1"), "482913"})
	if !o.neitherConfirmed() {
		t.Fatalf("relayed pairing confirmed: joiner=%v inviter=%v", o.joinErr, o.inviteErr)
	}
}

func relay(dst, src *tls.Conn) {
	_, _ = io.Copy(dst, src)
	dst.Close()
}

// The binding is the same at both ends of one connection and different on
// the next one between the same certificates, so nothing replays across.
func TestBindingIsPerConnection(t *testing.T) {
	jc, ic := selfSigned(t, "joiner"), selfSigned(t, "inviter")
	j1, i1 := connect(t, jc, ic)
	j2, _ := connect(t, jc, ic)
	a, b, c := binding(t, j1, "inv-1"), binding(t, i1, "inv-1"), binding(t, j2, "inv-1")
	if !sameBinding(a, b) {
		t.Fatal("the two ends of one connection disagree")
	}
	if string(a.EKM) == string(c.EKM) {
		t.Fatal("two connections share a channel binding")
	}
}

func TestBindingNeedsACompletedHandshake(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if _, err := Client(a, selfSigned(t, "joiner")).Binding([]byte("inv-1")); err == nil {
		t.Fatal("a binding before the handshake was accepted")
	}
}

func TestOversizedFrameIsRefused(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() { _, _ = a.Write([]byte{0, 0, 0x10, 0x00}) }() // 4096 > maxFrame
	if _, err := (StreamFrames{RW: b}).Recv(); err == nil {
		t.Fatal("an oversized frame was accepted")
	}
	if err := (StreamFrames{RW: a}).Send(make([]byte, maxFrame+1)); err == nil {
		t.Fatal("an oversized frame was sent")
	}
}
