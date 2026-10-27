package pake_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/pake"
)

func TestCPaceAcceptanceMutualConfirmationAndDurableOrdering(t *testing.T) {
	c := newCase()
	r := exchange(t, c)
	requireOK(t, r.joinErr)
	requireOK(t, r.inviteErr)
	requireEqual(t, r.persistCalls, 1)
	requireEqual(t, r.joinResult.Peer, c.joinBinding.Inviter)
	requireEqual(t, r.inviteResult.Peer, c.joinBinding.Joiner)
	requireEqual(t, bytes.Equal(r.joinResult.TranscriptID, r.inviteResult.TranscriptID), true)
	requireEqual(t, len(r.joinResult.TranscriptID) > 0, true)
	requireEqual(t, len(r.joinResult.TranscriptID) <= peer.MaxTranscriptIDBytes, true)
	requireEqual(t, len(r.joinSent), 2)
	requireEqual(t, len(r.inviteSent), 1)
	for _, frame := range append(r.joinSent, r.inviteSent...) {
		requireEqual(t, len(frame) <= peer.MaxFrameBytes, true)
	}
}
func TestCPaceAcceptanceEverySharedBindingFieldMustMatch(t *testing.T) {
	cases := map[string]func(*peer.ExchangeBinding){
		"invitation":        func(b *peer.ExchangeBinding) { b.InvitationID += "x" },
		"joiner-operation":  func(b *peer.ExchangeBinding) { b.JoinerOperationID += "x" },
		"inviter-operation": func(b *peer.ExchangeBinding) { b.InviterOperationID += "x" },
		"inviter-identity":  func(b *peer.ExchangeBinding) { b.Inviter = "sha256:" + strings.Repeat("3", 64) },
		"joiner-identity":   func(b *peer.ExchangeBinding) { b.Joiner = "sha256:" + strings.Repeat("4", 64) },
		"name":              func(b *peer.ExchangeBinding) { b.JoinerName = "Mallory" },
		"channel":           func(b *peer.ExchangeBinding) { b.ChannelBinding[0] ^= 1 },
		"roles":             func(b *peer.ExchangeBinding) { b.Inviter, b.Joiner = b.Joiner, b.Inviter },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := newCase()
			mutate(&c.inviteBinding)
			r := exchange(t, c)
			requireFailure(t, r.joinErr)
			requireFailure(t, r.inviteErr)
			requireEqual(t, r.persistCalls, 0)
			requireEqual(t, r.inviteResult.Peer, "")
		})
	}
}
func TestCPaceAcceptanceLengthPrefixesPreventContextCollision(t *testing.T) {
	c := newCase()
	c.joinBinding.InvitationID = "ab"
	c.joinBinding.JoinerOperationID = "c"
	c.inviteBinding.InvitationID = "a"
	c.inviteBinding.JoinerOperationID = "bc"
	r := exchange(t, c)
	requireFailure(t, r.joinErr)
	requireFailure(t, r.inviteErr)
	requireEqual(t, r.persistCalls, 0)
}
func TestCPaceAcceptanceWrongCodeCannotConfirm(t *testing.T) {
	c := newCase()
	c.joinCode = []byte("928375")
	r := exchange(t, c)
	requireFailure(t, r.joinErr)
	requireFailure(t, r.inviteErr)
	requireEqual(t, r.persistCalls, 0)
}
func TestCPaceAcceptancePersistFailureSendsNoFinalConfirmation(t *testing.T) {
	c := newCase()
	c.persistErr = errors.New("disk failure")
	r := exchange(t, c)
	requireEqual(t, errors.Is(r.joinErr, c.persistErr), true)
	requireFailure(t, r.inviteErr)
	requireEqual(t, r.persistCalls, 1)
	requireEqual(t, len(r.joinSent), 1)
	requireEqual(t, r.inviteResult.Peer, "")
}
func TestCPaceAcceptanceInvalidBindingAndMissingHookHaveNoIO(t *testing.T) {
	cases := map[string]func(*peer.ExchangeBinding){
		"empty-channel": func(b *peer.ExchangeBinding) { b.ChannelBinding = nil },
		"large-channel": func(b *peer.ExchangeBinding) { b.ChannelBinding = make([]byte, 65) },
		"bad-identity":  func(b *peer.ExchangeBinding) { b.Inviter = "bad" },
		"self":          func(b *peer.ExchangeBinding) { b.Inviter = b.Joiner },
		"empty-id":      func(b *peer.ExchangeBinding) { b.JoinerOperationID = "" },
		"long-id":       func(b *peer.ExchangeBinding) { b.InviterOperationID = strings.Repeat("x", 129) },
		"bad-name":      func(b *peer.ExchangeBinding) { b.JoinerName = "Bob\nMallory" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := binding()
			mutate(&b)
			f := &scriptFrames{}
			ex := pake.New()
			err := ex.Join(context.Background(), f, b, []byte("928374"), func(context.Context, peer.ExchangeResult) error { t.Error("invalid input reached persist"); return nil })
			requireFailure(t, err)
			_, err = ex.Invite(context.Background(), f, b, []byte("928374"))
			requireFailure(t, err)
			requireEqual(t, len(f.sent), 0)
			requireEqual(t, f.reads, 0)
		})
	}
	f := &scriptFrames{}
	err := pake.New().Join(context.Background(), f, binding(), []byte("928374"), nil)
	requireEqual(t, errors.Is(err, peer.ErrInvalidBinding), true)
	requireEqual(t, len(f.sent), 0)
	requireEqual(t, f.reads, 0)
}
func TestCPaceAcceptanceCancelledContextHasNoIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &scriptFrames{}
	ex := pake.New()
	err := ex.Join(ctx, f, binding(), []byte("928374"), func(context.Context, peer.ExchangeResult) error { t.Error("cancelled call persisted"); return nil })
	requireEqual(t, errors.Is(err, context.Canceled), true)
	_, err = ex.Invite(ctx, f, binding(), []byte("928374"))
	requireEqual(t, errors.Is(err, context.Canceled), true)
	requireEqual(t, len(f.sent), 0)
	requireEqual(t, f.reads, 0)
}
