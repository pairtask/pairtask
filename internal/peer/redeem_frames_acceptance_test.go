//go:build unix

package peer_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

type redeemScript struct {
	input [][]byte
	sent  [][]byte
	reads int
}

func (f *redeemScript) Send(b []byte) error { f.sent = append(f.sent, bytes.Clone(b)); return nil }
func (f *redeemScript) Recv() ([]byte, error) {
	f.reads++
	if len(f.input) == 0 {
		return nil, io.EOF
	}
	b := f.input[0]
	f.input = f.input[1:]
	return bytes.Clone(b), nil
}

// Independent framing keeps adversarial inputs independent of production encoders.
func redeemWire(kind byte, fields ...[]byte) []byte {
	out := []byte{1, kind}
	for _, field := range fields {
		out = binary.BigEndian.AppendUint16(out, uint16(len(field)))
		out = append(out, field...)
	}
	return out
}
func TestRedeemAcceptanceMalformedHelloDoesNotCharge(t *testing.T) {
	valid := redeemWire(1, []byte("operation"), []byte("Bob"))
	cases := [][]byte{nil, {2, 1}, valid[:len(valid)-1], append(bytes.Clone(valid), 0), redeemWire(2, []byte("inv"), []byte("op")), make([]byte, 4097), redeemWire(1, nil, []byte("Bob")), redeemWire(1, []byte("op"), []byte("bad\nname"))}
	for n, frame := range cases {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			r := newRedeemFixture(t)
			f := &redeemScript{input: [][]byte{frame}}
			i, err := peer.NewInviter(r.inviteEx, r.inv.manager, r.alice)
			invOK(t, err)
			_, err = i.Serve(context.Background(), f, peer.Connection{PeerLeaf: r.bob.Fingerprint(), ChannelBinding: r.joinBinding})
			invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
			invEqual(t, invitationStatus(t, r.inv).AttemptsLeft, 3)
			redeemNoOutcome(t, r)
		})
	}
}
func TestRedeemAcceptanceFrameCodecStrictShapeAndLimits(t *testing.T) {
	cases := []struct {
		name   string
		wire   []byte
		decode func([]byte) error
	}{
		{"joiner", redeemWire(1, []byte("op"), []byte("Bob")), func(b []byte) error { _, err := peer.DecodeJoinerHello(b); return err }},
		{"inviter", redeemWire(2, []byte("inv"), []byte("op")), func(b []byte) error { _, err := peer.DecodeInviterHello(b); return err }},
		{"refusal", redeemWire(3, []byte("busy")), func(b []byte) error { _, err := peer.DecodeRefusal(b); return err }},
		{"receipt", redeemWire(4, []byte("inv"), []byte("op"), []byte("joiner"), []byte("transcript")), func(b []byte) error { _, err := peer.DecodeReceipt(b); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			invOK(t, c.decode(c.wire))
			badVersion := bytes.Clone(c.wire)
			badVersion[0] = 2
			badType := bytes.Clone(c.wire)
			badType[1] = 99
			for _, frame := range [][]byte{badVersion, badType, c.wire[:len(c.wire)-1], append(bytes.Clone(c.wire), 0), make([]byte, 4097)} {
				invEqual(t, errors.Is(c.decode(frame), peer.ErrPairingProtocol), true)
			}
		})
	}
	frame, err := peer.EncodeRefusal(peer.Refusal{Reason: strings.Repeat("x", 4092)})
	invOK(t, err)
	invEqual(t, len(frame), 4096)
	_, err = peer.EncodeRefusal(peer.Refusal{Reason: strings.Repeat("x", 4093)})
	invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
	_, err = peer.EncodeRefusal(peer.Refusal{Reason: strings.Repeat("x", 65536)})
	invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
}

type redeemOversizedExchange struct {
	mode   string
	called bool
}

func (x *redeemOversizedExchange) Join(ctx context.Context, f peer.Frames, b peer.ExchangeBinding, code []byte, persist func(context.Context, peer.ExchangeResult) error) error {
	x.called = true
	if x.mode == "send" {
		return f.Send(make([]byte, 4097))
	}
	_, err := f.Recv()
	return err
}
func (x *redeemOversizedExchange) Invite(ctx context.Context, f peer.Frames, b peer.ExchangeBinding, code []byte) (peer.ExchangeResult, error) {
	err := x.Join(ctx, f, b, code, nil)
	return peer.ExchangeResult{}, err
}
func TestRedeemAcceptanceProviderFramesAreBoundedBothWays(t *testing.T) {
	for _, c := range [][2]string{{"joiner", "send"}, {"joiner", "receive"}, {"inviter", "send"}, {"inviter", "receive"}} {
		t.Run(c[0]+"/"+c[1], func(t *testing.T) { redeemOversizeCase(t, c[0], c[1]) })
	}
}
func redeemOversizeCase(t *testing.T, side, mode string) {
	t.Helper()
	r := newRedeemFixture(t)
	ex := &redeemOversizedExchange{mode: mode}
	f := &redeemScript{}
	var err error
	if side == "joiner" {
		f.input = [][]byte{redeemWire(2, []byte("inv"), []byte("remote")), make([]byte, 4097)}
		j, e := peer.NewJoiner(ex, r.joins, r.bob, r.inv.clock.read, rand.Reader)
		invOK(t, e)
		_, err = j.Join(context.Background(), f, peer.JoinInput{Connection: peer.Connection{PeerLeaf: r.alice.Fingerprint(), ChannelBinding: r.joinBinding}, Code: r.code(), Name: "Bob"})
	} else {
		f.input = [][]byte{redeemWire(1, []byte("op"), []byte("Bob")), make([]byte, 4097)}
		i, e := peer.NewInviter(ex, r.inv.manager, r.alice)
		invOK(t, e)
		_, err = i.Serve(context.Background(), f, peer.Connection{PeerLeaf: r.bob.Fingerprint(), ChannelBinding: r.inviteBinding})
	}
	invEqual(t, ex.called, true)
	invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
	for _, sent := range f.sent {
		invEqual(t, len(sent) <= 4096, true)
	}
	redeemNoOutcome(t, r)
}

func TestRedeemAcceptanceCodeGroupingAndInvalidInputs(t *testing.T) {
	t.Run("grouped", func(t *testing.T) {
		r := newRedeemFixture(t)
		result := r.run(t, []byte(" "+r.open.Code+" "))
		invOK(t, result.joinErr)
		invOK(t, result.inviteErr)
	})
	for n, code := range []string{"", "123", "1234567890123456789", "123a56", "123\n456", "１２３４５６", strings.Repeat(" ", 65) + "123456"} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			r := newRedeemFixture(t)
			j, err := peer.NewJoiner(r.joinEx, r.joins, r.bob, r.inv.clock.read, rand.Reader)
			invOK(t, err)
			f := &redeemScript{}
			_, err = j.Join(context.Background(), f, peer.JoinInput{Connection: peer.Connection{PeerLeaf: r.alice.Fingerprint(), ChannelBinding: r.joinBinding}, Code: []byte(code), Name: "Bob"})
			invEqual(t, errors.Is(err, peer.ErrInvalidBinding), true)
			invEqual(t, len(f.sent), 0)
			invEqual(t, f.reads, 0)
			invEqual(t, len(redeemRows(t, r.joins)), 0)
		})
	}
}

func TestRedeemAcceptanceJoinInputRedactsCode(t *testing.T) {
	in := peer.JoinInput{Connection: peer.Connection{PeerLeaf: "sha256:" + strings.Repeat("a", 64), ChannelBinding: []byte("channel-binding")}, Code: []byte("928374"), Name: "Bob"}
	encoded, err := json.Marshal(in)
	invOK(t, err)
	outputs := []string{string(encoded), fmt.Sprintf("%v", in), fmt.Sprintf("%+v", in), fmt.Sprintf("%#v", in)}
	for _, out := range outputs {
		invEqual(t, strings.Contains(out, "928374"), false)
		invEqual(t, strings.Contains(out, "57 50 56 51 55 52"), false)
	}
	var object map[string]any
	invOK(t, json.Unmarshal(encoded, &object))
	_, hasCode := object["Code"]
	invEqual(t, hasCode, false)
}
