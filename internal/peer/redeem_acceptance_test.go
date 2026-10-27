//go:build unix

package peer_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestRedeemAcceptancePersistsBeforeConfirmationAndReceipt(t *testing.T) {
	r := newRedeemFixture(t)
	r.joinEx.afterPersist = r.checkVerified
	r.filter = r.checkDurableReceipt
	result := r.run(t, r.code())
	invOK(t, result.joinErr)
	invOK(t, result.inviteErr)
	rows := redeemRows(t, r.joins)
	invEqual(t, len(rows), 1)
	row := rows[0]
	invEqual(t, row.State, peer.JoinAwaitingAcceptance)
	invEqual(t, row.Peer, r.alice.Fingerprint())
	invEqual(t, row.Joiner, r.bob.Fingerprint())
	outcome, found, err := r.inv.store.Outcome(context.Background(), row.InviterOperationID)
	invOK(t, err)
	invEqual(t, found, true)
	invEqual(t, outcome.IdentityKey, r.bob.Fingerprint())
	invEqual(t, bytes.Equal(outcome.TranscriptID, row.TranscriptID), true)
	invEqual(t, outcome.InvitationID, row.InvitationID)
	invEqual(t, invitationStatus(t, r.inv).Consumed, true)
}

type redeemFaultStore struct {
	peer.JoinStore
	verifyErr, receiptErr error
}

func (s redeemFaultStore) RecordVerified(ctx context.Context, id string, res peer.ExchangeResult, at time.Time) (peer.JoinRecord, error) {
	if s.verifyErr != nil {
		return peer.JoinRecord{}, s.verifyErr
	}
	return s.JoinStore.RecordVerified(ctx, id, res, at)
}
func (s redeemFaultStore) RecordReceipt(ctx context.Context, id string, r peer.JoinReceipt, at time.Time) (peer.JoinRecord, error) {
	if s.receiptErr != nil {
		return peer.JoinRecord{}, s.receiptErr
	}
	return s.JoinStore.RecordReceipt(ctx, id, r, at)
}
func TestRedeemAcceptanceFailedLocalPersistencePreventsConfirmation(t *testing.T) {
	r := newRedeemFixture(t)
	r.joins = redeemFaultStore{JoinStore: r.joins, verifyErr: errors.New("disk failure")}
	result := r.run(t, r.code())
	invError(t, result.joinErr)
	invEqual(t, r.joinEx.confirmations, 0)
	redeemNoOutcome(t, r)
	invEqual(t, redeemRows(t, r.joins)[0].State, peer.JoinFailed)
}
func TestRedeemAcceptanceLostReceiptKeepsImmutableUnknownOutcome(t *testing.T) {
	for _, failure := range []string{"drop", "corrupt", "store"} {
		t.Run(failure, func(t *testing.T) {
			r := newRedeemFixture(t)
			r.filter = func(frame []byte) ([]byte, error) {
				if len(frame) < 2 || frame[1] != peer.FrameReceipt {
					return frame, nil
				}
				if failure == "drop" {
					return nil, io.ErrClosedPipe
				}
				if failure == "corrupt" {
					frame[len(frame)-1] ^= 1
				}
				return frame, nil
			}
			if failure == "store" {
				r.joins = redeemFaultStore{JoinStore: r.joins, receiptErr: errors.New("receipt disk failure")}
			}
			result := r.run(t, r.code())
			invEqual(t, errors.Is(result.joinErr, peer.ErrJoinOutcomeUnknown), true)
			row := redeemRows(t, r.joins)[0]
			invEqual(t, row.State, peer.JoinOutcomeUnknown)
			invEqual(t, row.Peer, r.alice.Fingerprint())
			invEqual(t, len(row.TranscriptID) > 0, true)
			_, found, err := r.inv.store.Outcome(context.Background(), row.InviterOperationID)
			invOK(t, err)
			invEqual(t, found, true)
			_, err = r.joins.RecoverJoins(context.Background(), r.inv.clock.read())
			invOK(t, err)
			invEqual(t, redeemRows(t, r.joins)[0].State, peer.JoinOutcomeUnknown)
		})
	}
}
func TestRedeemAcceptanceProviderErrorAfterPersistStaysUnknown(t *testing.T) {
	r := newRedeemFixture(t)
	r.joinEx.afterPersist = func(peer.ExchangeBinding) error { return peer.ErrExchangeFailed }
	result := r.run(t, r.code())
	invEqual(t, errors.Is(result.joinErr, peer.ErrJoinOutcomeUnknown), true)
	invEqual(t, redeemRows(t, r.joins)[0].State, peer.JoinOutcomeUnknown)
	redeemNoOutcome(t, r)
}

func TestRedeemAcceptanceWrongCodeExhaustsWithoutOutcome(t *testing.T) {
	r := newRedeemFixture(t)
	wrong := r.code()
	wrong[0] = '0' + (wrong[0]-'0'+1)%10
	for n := 0; n < 3; n++ {
		result := r.run(t, wrong)
		invError(t, result.joinErr)
		invEqual(t, invitationStatus(t, r.inv).AttemptsLeft, 2-n)
		redeemNoOutcome(t, r)
	}
	result := r.run(t, r.code())
	invError(t, result.joinErr)
	invEqual(t, errors.Is(result.inviteErr, peer.ErrInvitationExhausted), true)
	invEqual(t, invitationStatus(t, r.inv).Consumed, false)
}
func TestRedeemAcceptanceBindingAndAuthenticatedIdentityMustMatch(t *testing.T) {
	for _, fault := range []string{"binding", "joiner-peer", "inviter-peer"} {
		t.Run(fault, func(t *testing.T) {
			r := newRedeemFixture(t)
			switch fault {
			case "binding":
				r.joinBinding[0] ^= 1
			case "joiner-peer":
				r.joinEx.peerOverride = "sha256:" + strings.Repeat("a", 64)
			case "inviter-peer":
				r.inviteEx.peerOverride = "sha256:" + strings.Repeat("b", 64)
			}
			result := r.run(t, r.code())
			invError(t, result.joinErr)
			redeemNoOutcome(t, r)
			invEqual(t, invitationStatus(t, r.inv).Consumed, false)
		})
	}
}

func TestRedeemAcceptanceUnavailableAndSelfRefuseBeforeSideEffects(t *testing.T) {
	for _, which := range []string{"unavailable", "self", "empty-binding", "large-binding", "bad-peer"} {
		t.Run(which, func(t *testing.T) {
			r := newRedeemFixture(t)
			var ex peer.Exchanger = r.joinEx
			connection := peer.Connection{PeerLeaf: r.bob.Fingerprint(), ChannelBinding: r.joinBinding}
			switch which {
			case "unavailable":
				ex = nil
			case "self":
				connection.PeerLeaf = r.alice.Fingerprint()
			case "empty-binding":
				connection.ChannelBinding = nil
			case "large-binding":
				connection.ChannelBinding = make([]byte, 65)
			case "bad-peer":
				connection.PeerLeaf = "not-a-fingerprint"
			}
			f := &redeemScript{}
			inviter, err := peer.NewInviter(ex, r.inv.manager, r.alice, rand.Reader)
			invOK(t, err)
			_, err = inviter.Serve(context.Background(), f, connection)
			invError(t, err)
			invEqual(t, f.reads, 0)
			invEqual(t, len(f.sent), 0)
			invEqual(t, invitationStatus(t, r.inv).AttemptsLeft, 3)
			joiner, err := peer.NewJoiner(ex, r.joins, r.alice, r.inv.clock.read, rand.Reader)
			invOK(t, err)
			_, err = joiner.Join(context.Background(), f, peer.JoinInput{Connection: connection, Code: r.code(), Name: "Alice"})
			invError(t, err)
			invEqual(t, len(redeemRows(t, r.joins)), 0)
			invEqual(t, len(f.sent), 0)
		})
	}
}

func TestRedeemAcceptanceCancelAndDeadlineBeforeCommit(t *testing.T) {
	for _, fault := range []string{"cancel", "timeout", "replace"} {
		t.Run(fault, func(t *testing.T) {
			r := newRedeemFixture(t)
			var hookErr error
			r.inviteEx.beforeCommit = func() {
				switch fault {
				case "cancel":
					hookErr = r.inv.manager.Cancel(r.open.ID)
				case "timeout":
					r.inv.clock.advance(31 * time.Second)
				case "replace":
					_, hookErr = r.inv.manager.Open()
				}
			}
			result := r.run(t, r.code())
			invOK(t, hookErr)
			invError(t, result.inviteErr)
			invEqual(t, errors.Is(result.joinErr, peer.ErrJoinOutcomeUnknown), true)
			redeemNoOutcome(t, r)
		})
	}
}

func TestRedeemAcceptanceBusyRefusesWithoutAnotherCharge(t *testing.T) {
	r := newRedeemFixture(t)
	active := beginInvitation(t, r.inv, r.open.ID)
	result := r.run(t, r.code())
	invEqual(t, errors.Is(result.inviteErr, peer.ErrInvitationBusy), true)
	invEqual(t, errors.Is(result.joinErr, peer.ErrJoinRefused), true)
	var refusal *peer.JoinRefusal
	invEqual(t, errors.As(result.joinErr, &refusal), true)
	invEqual(t, refusal.Reason, "invitation_busy")
	invEqual(t, invitationStatus(t, r.inv).AttemptsLeft, 2)
	invEqual(t, invitationStatus(t, r.inv).InFlight, true)
	invOK(t, active.Fail())
	redeemNoOutcome(t, r)
}
