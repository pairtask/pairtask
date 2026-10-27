package pake_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/pake"
)

func TestCPaceAcceptanceTamperedMessagesCannotConfirm(t *testing.T) {
	for _, message := range []string{"A", "B", "C"} {
		t.Run(message, func(t *testing.T) {
			c := newCase()
			c.mutateJoin = func(n int, b []byte) ([]byte, error) {
				if message == "A" && n == 1 {
					b[0] ^= 1
				}
				if message == "C" && n == 2 {
					b[len(b)-1] ^= 1
				}
				return b, nil
			}
			c.mutateInvite = func(_ int, b []byte) ([]byte, error) {
				if message == "B" {
					b[len(b)-1] ^= 1
				}
				return b, nil
			}
			r := exchange(t, c)
			requireFailure(t, r.inviteErr)
			requireEqual(t, r.inviteResult.Peer, "")
			if message == "C" {
				requireEqual(t, r.persistCalls, 1)
				return
			}
			requireFailure(t, r.joinErr)
			requireEqual(t, r.persistCalls, 0)
		})
	}
}
func TestCPaceAcceptanceOldMessagesCannotBeReplayed(t *testing.T) {
	c := newCase()
	old := exchange(t, c)
	requireOK(t, old.joinErr)
	requireOK(t, old.inviteErr)
	j := &scriptFrames{input: [][]byte{old.inviteSent[0]}}
	calls := 0
	err := pake.New().Join(context.Background(), j, c.joinBinding, c.joinCode, func(context.Context, peer.ExchangeResult) error { calls++; return nil })
	requireFailure(t, err)
	requireEqual(t, calls, 0)
	requireEqual(t, len(j.sent), 1)
	i := &scriptFrames{input: [][]byte{old.joinSent[0], old.joinSent[1]}}
	r, err := pake.New().Invite(context.Background(), i, c.inviteBinding, c.inviteCode)
	requireFailure(t, err)
	requireEqual(t, r.Peer, "")
}
func TestCPaceAcceptanceOversizedInputRejectedByAdapter(t *testing.T) {
	join := &scriptFrames{input: [][]byte{make([]byte, 4097)}}
	err := pake.New().Join(context.Background(), join, binding(), []byte("928374"), func(context.Context, peer.ExchangeResult) error { t.Error("oversized input persisted"); return nil })
	requireEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
	invite := &scriptFrames{input: [][]byte{make([]byte, 4097)}}
	_, err = pake.New().Invite(context.Background(), invite, binding(), []byte("928374"))
	requireEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
	requireEqual(t, len(invite.sent), 0)
}
func TestCPaceAcceptanceSendFailureAfterPersistRetainsResult(t *testing.T) {
	c := newCase()
	c.mutateJoin = func(n int, b []byte) ([]byte, error) {
		if n == 2 {
			return nil, io.ErrClosedPipe
		}
		return b, nil
	}
	r := exchange(t, c)
	requireFailure(t, r.joinErr)
	requireFailure(t, r.inviteErr)
	requireEqual(t, r.persistCalls, 1)
	requireEqual(t, r.joinResult.Peer, c.joinBinding.Inviter)
	requireEqual(t, len(r.joinResult.TranscriptID) > 0, true)
}
func TestCPaceAcceptanceMaximumBindingFieldsFit(t *testing.T) {
	c := newCase()
	c.joinBinding.InvitationID = string(bytes.Repeat([]byte{'i'}, 128))
	c.joinBinding.JoinerOperationID = string(bytes.Repeat([]byte{'j'}, 128))
	c.joinBinding.InviterOperationID = string(bytes.Repeat([]byte{'a'}, 128))
	c.joinBinding.JoinerName = string(bytes.Repeat([]byte{'B'}, 64))
	c.joinBinding.ChannelBinding = bytes.Repeat([]byte{7}, 64)
	c.inviteBinding = c.joinBinding
	r := exchange(t, c)
	requireOK(t, r.joinErr)
	requireOK(t, r.inviteErr)
	requireEqual(t, bytes.Equal(r.joinResult.TranscriptID, r.inviteResult.TranscriptID), true)
}
