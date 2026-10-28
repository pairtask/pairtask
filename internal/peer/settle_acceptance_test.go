//go:build unix

package peer_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestSettleAcceptanceDecisionDurableBeforeAckAndConfirmation(t *testing.T) {
	for _, decision := range []peer.PairingState{peer.PairingAccepted, peer.PairingRejected} {
		t.Run(string(decision), func(t *testing.T) {
			r, row := settleReady(t, decision)
			beforeJoin, beforeInvite := r.joinEx.confirmations, r.inviteEx.invites
			joinCheck := func(frame []byte) ([]byte, error) {
				if frame[1] == peer.FrameAck {
					saved, _, err := r.joins.GetJoin(context.Background(), row.OperationID)
					if err != nil {
						return nil, err
					}
					if saved.State == peer.JoinAwaitingAcceptance {
						return nil, errors.New("ack preceded decision")
					}
					if !saved.DecisionConfirmedAt.IsZero() {
						return nil, errors.New("confirmation preceded receipt")
					}
				}
				return frame, nil
			}
			inviteCheck := func(frame []byte) ([]byte, error) {
				if frame[1] == peer.FrameAckReceipt {
					saved, _, err := r.inv.store.Request(context.Background(), r.bob.Fingerprint())
					if err != nil {
						return nil, err
					}
					if saved.DeliveredAt.IsZero() {
						return nil, errors.New("receipt preceded delivery commit")
					}
				}
				return frame, nil
			}
			result := settlePair(t, r, row.OperationID, joinCheck, inviteCheck)
			invOK(t, result.joinErr)
			invOK(t, result.inviteErr)
			invEqual(t, result.row.DecisionConfirmedAt.IsZero(), false)
			want := 0
			if decision == peer.PairingAccepted {
				want = 1
			}
			settleContactCount(t, r.joins, want)
			settleContactCount(t, r.inv.store, want)
			invEqual(t, r.joinEx.confirmations, beforeJoin)
			invEqual(t, r.inviteEx.invites, beforeInvite)
		})
	}
}

func TestSettleAcceptanceLostFramesConvergeWithoutNewPairing(t *testing.T) {
	for _, lost := range []byte{peer.FrameSettle, peer.FrameSettleReply, peer.FrameAck, peer.FrameAckReceipt} {
		t.Run(string(rune('0'+lost)), func(t *testing.T) {
			r, row := settleReady(t, peer.PairingAccepted)
			drop := func(frame []byte) ([]byte, error) {
				if frame[1] == lost {
					return nil, io.ErrUnexpectedEOF
				}
				return frame, nil
			}
			first := settlePair(t, r, row.OperationID, drop, drop)
			if first.joinErr == nil {
				t.Fatal("lost frame reported confirmed success")
			}
			saved := settleStored(t, r, row.OperationID)
			invEqual(t, saved.DecisionConfirmedAt.IsZero(), true)
			if lost == peer.FrameAck {
				invEqual(t, settleRequest(t, r).DeliveredAt.IsZero(), true)
			}
			if lost == peer.FrameAckReceipt {
				invEqual(t, settleRequest(t, r).DeliveredAt.IsZero(), false)
			}
			// New Joiner and Inviter instances model worker restart over the same stores.
			second := settlePair(t, r, row.OperationID, nil, nil)
			invOK(t, second.joinErr)
			invOK(t, second.inviteErr)
			invEqual(t, second.row.State, peer.JoinContact)
			invEqual(t, second.row.DecisionConfirmedAt.IsZero(), false)
			settleContactCount(t, r.joins, 1)
			settleContactCount(t, r.inv.store, 1)
			invEqual(t, r.joinEx.confirmations, 1)
			invEqual(t, r.inviteEx.invites, 1)
		})
	}
}

func TestSettleAcceptanceLostOriginalReceiptRecoversWithoutCode(t *testing.T) {
	r := newRedeemFixture(t)
	r.filter = func(frame []byte) ([]byte, error) {
		if frame[1] == peer.FrameReceipt {
			return nil, io.ErrUnexpectedEOF
		}
		return frame, nil
	}
	paired := r.run(t, r.code())
	invEqual(t, paired.row.State, peer.JoinOutcomeUnknown)
	settled := settlePair(t, r, paired.row.OperationID, nil, nil)
	invOK(t, settled.joinErr)
	invOK(t, settled.inviteErr)
	invEqual(t, settled.row.State, peer.JoinAwaitingAcceptance)
	invEqual(t, settled.row.DecisionConfirmedAt.IsZero(), true)
	settleContactCount(t, r.joins, 0)
	settleContactCount(t, r.inv.store, 0)
	invEqual(t, r.joinEx.confirmations, 1)
	invEqual(t, r.inviteEx.invites, 1)
}

func TestSettleAcceptanceUnknownOutcomeStaysUnknown(t *testing.T) {
	r := newRedeemFixture(t)
	r.joinEx.afterPersist = func(peer.ExchangeBinding) error { return io.ErrUnexpectedEOF }
	paired := r.run(t, r.code())
	invEqual(t, paired.row.State, peer.JoinOutcomeUnknown)
	settled := settlePair(t, r, paired.row.OperationID, nil, nil)
	invOK(t, settled.joinErr)
	invOK(t, settled.inviteErr)
	invEqual(t, settled.row.State, peer.JoinOutcomeUnknown)
	redeemNoOutcome(t, r)
	settleContactCount(t, r.joins, 0)
}

func TestSettleAcceptanceChangedPinSendsNothing(t *testing.T) {
	r, row := settleReady(t, peer.PairingAccepted)
	frames := &settleScript{}
	_, err := settleJoiner(t, r).Settle(context.Background(), frames, peer.Connection{PeerLeaf: r.bob.Fingerprint()}, row.OperationID)
	invEqual(t, errors.Is(err, peer.ErrPeerMismatch), true)
	invEqual(t, len(frames.sent), 0)
	invEqual(t, settleStored(t, r, row.OperationID).State, peer.JoinAwaitingAcceptance)
}

func TestSettleAcceptanceConfirmedTerminalDoesNoNetworkIO(t *testing.T) {
	r, row := settleReady(t, peer.PairingAccepted)
	settled := settlePair(t, r, row.OperationID, nil, nil)
	invOK(t, settled.joinErr)
	frames := &settleScript{}
	again, err := settleJoiner(t, r).Settle(context.Background(), frames, peer.Connection{PeerLeaf: r.alice.Fingerprint()}, row.OperationID)
	invOK(t, err)
	invEqual(t, again.DecisionConfirmedAt, settled.row.DecisionConfirmedAt)
	invEqual(t, len(frames.sent), 0)
}

func TestSettleAcceptanceInviterMatchesEveryDurableFieldAndTLSPeer(t *testing.T) {
	r, row := settleReady(t, peer.PairingAccepted)
	base := peer.Settle{JoinerOperationID: row.OperationID, InviterOperationID: row.InviterOperationID, InvitationID: row.InvitationID, TranscriptID: row.TranscriptID}
	mutations := map[string]func(*peer.Settle){
		"invitation":        func(s *peer.Settle) { s.InvitationID += "x" },
		"inviter operation": func(s *peer.Settle) { s.InviterOperationID += "x" },
		"transcript":        func(s *peer.Settle) { s.TranscriptID = []byte("wrong") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			frame, err := peer.EncodeSettle(changed)
			invOK(t, err)
			assertSettleUnknown(t, r, frame, r.bob.Fingerprint())
		})
	}
	assertSettleUnknown(t, r, settleFrame(t, row), r.alice.Fingerprint())
	invEqual(t, settleRequest(t, r).DeliveredAt.IsZero(), true)
	settleContactCount(t, r.inv.store, 0)
}

func assertSettleUnknown(t *testing.T, r *redeemFixture, frame []byte, pin string) {
	t.Helper()
	frames := &settleScript{in: [][]byte{frame}}
	invOK(t, settleInviter(t, r).ServeSettle(context.Background(), frames, peer.Connection{PeerLeaf: pin}))
	invEqual(t, len(frames.sent), 1)
	reply, err := peer.DecodeSettleReply(frames.sent[0])
	invOK(t, err)
	invEqual(t, reply.Receipt, byte(peer.ReceiptUnknown))
}

func TestSettleAcceptanceAckMustMatchThisConnectionDecision(t *testing.T) {
	for _, ack := range []peer.Ack{{JoinerOperationID: "other", Decision: peer.PairingAccepted}, {Decision: peer.PairingRejected}} {
		r, row := settleReady(t, peer.PairingAccepted)
		if ack.JoinerOperationID == "" {
			ack.JoinerOperationID = row.OperationID
		}
		encoded, err := peer.EncodeAck(ack)
		invOK(t, err)
		frames := &settleScript{in: [][]byte{settleFrame(t, row), encoded}}
		err = settleInviter(t, r).ServeSettle(context.Background(), frames, peer.Connection{PeerLeaf: r.bob.Fingerprint()})
		invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
		invEqual(t, settleRequest(t, r).DeliveredAt.IsZero(), true)
		settleContactCount(t, r.inv.store, 0)
	}
}

func TestSettleAcceptanceFinalReceiptMustMatchOperationAndDecision(t *testing.T) {
	for _, alterDecision := range []bool{false, true} {
		r, row := settleReady(t, peer.PairingAccepted)
		mutate := func(frame []byte) ([]byte, error) {
			if frame[1] != peer.FrameAckReceipt {
				return frame, nil
			}
			receipt, err := peer.DecodeAckReceipt(frame)
			if err != nil {
				return nil, err
			}
			if alterDecision {
				receipt.Decision = peer.PairingRejected
			} else {
				receipt.JoinerOperationID += "x"
			}
			return peer.EncodeAckReceipt(receipt)
		}
		result := settlePair(t, r, row.OperationID, nil, mutate)
		invEqual(t, errors.Is(result.joinErr, peer.ErrPairingProtocol), true)
		invEqual(t, settleStored(t, r, row.OperationID).DecisionConfirmedAt.IsZero(), true)
		invOK(t, settlePair(t, r, row.OperationID, nil, nil).joinErr)
	}
}

func TestSettleAcceptanceCodecsRejectTruncationTrailingAndWrongKind(t *testing.T) {
	settle, err := peer.EncodeSettle(peer.Settle{JoinerOperationID: "join", InviterOperationID: "invite-op", InvitationID: "invite", TranscriptID: []byte("transcript")})
	invOK(t, err)
	reply, err := peer.EncodeSettleReply(peer.SettleReply{Receipt: peer.ReceiptMatched, Decision: peer.PairingAccepted})
	invOK(t, err)
	ack, err := peer.EncodeAck(peer.Ack{JoinerOperationID: "join", Decision: peer.PairingAccepted})
	invOK(t, err)
	receipt, err := peer.EncodeAckReceipt(peer.AckReceipt{JoinerOperationID: "join", Decision: peer.PairingAccepted})
	invOK(t, err)
	checks := []struct {
		frame  []byte
		decode func([]byte) error
	}{
		{settle, func(b []byte) error { _, e := peer.DecodeSettle(b); return e }},
		{reply, func(b []byte) error { _, e := peer.DecodeSettleReply(b); return e }},
		{ack, func(b []byte) error { _, e := peer.DecodeAck(b); return e }},
		{receipt, func(b []byte) error { _, e := peer.DecodeAckReceipt(b); return e }},
	}
	for _, check := range checks {
		invOK(t, check.decode(check.frame))
		for n := 0; n < len(check.frame); n++ {
			invEqual(t, errors.Is(check.decode(check.frame[:n]), peer.ErrPairingProtocol), true)
		}
		invEqual(t, errors.Is(check.decode(append(bytes.Clone(check.frame), 0)), peer.ErrPairingProtocol), true)
		wrong := bytes.Clone(check.frame)
		wrong[1] = peer.FrameJoinerHello
		invEqual(t, errors.Is(check.decode(wrong), peer.ErrPairingProtocol), true)
	}
}

func TestSettleAcceptanceTerminalUnconfirmedNeverAcksUnknownOrChangedDecision(t *testing.T) {
	for _, reply := range []peer.SettleReply{
		{Receipt: peer.ReceiptUnknown, Decision: peer.PairingPending},
		{Receipt: peer.ReceiptMatched, Decision: peer.PairingPending},
		{Receipt: peer.ReceiptMatched, Decision: peer.PairingRejected},
	} {
		r, row := settleReady(t, peer.PairingAccepted)
		_, err := r.joins.RecordDecision(context.Background(), row.OperationID, peer.PairingAccepted, r.inv.clock.read())
		invOK(t, err)
		frame, err := peer.EncodeSettleReply(reply)
		invOK(t, err)
		frames := &settleScript{in: [][]byte{frame}}
		_, _ = settleJoiner(t, r).Settle(context.Background(), frames, peer.Connection{PeerLeaf: r.alice.Fingerprint()}, row.OperationID)
		invEqual(t, len(frames.sent), 1) // Settle only, never Ack for an unmatched decision.
		invEqual(t, settleStored(t, r, row.OperationID).State, peer.JoinContact)
		invEqual(t, settleStored(t, r, row.OperationID).DecisionConfirmedAt.IsZero(), true)
	}
}

func TestSettleAcceptanceDurableWriteFailuresRemainRetryable(t *testing.T) {
	for _, phase := range []string{"decision", "delivery", "confirmation"} {
		t.Run(phase, func(t *testing.T) {
			r, row := settleReady(t, peer.PairingAccepted)
			joins, pairs := r.joins, r.inv.store
			r.joins = &settleFaultJoins{JoinStore: joins, failDecision: phase == "decision", failConfirmation: phase == "confirmation"}
			r.inv = newInvitationFixture(t, &settleFaultPairs{PairingStore: pairs, fail: phase == "delivery"})
			first := settlePair(t, r, row.OperationID, nil, nil)
			if first.joinErr == nil {
				t.Fatal("failed durable write reported success")
			}
			saved := settleStored(t, r, row.OperationID)
			invEqual(t, saved.DecisionConfirmedAt.IsZero(), true)
			if phase == "decision" {
				invEqual(t, saved.State, peer.JoinAwaitingAcceptance)
				settleContactCount(t, joins, 0)
			}
			if phase != "confirmation" {
				invEqual(t, settleRequest(t, r).DeliveredAt.IsZero(), true)
				settleContactCount(t, pairs, 0)
			}
			second := settlePair(t, r, row.OperationID, nil, nil)
			invOK(t, second.joinErr)
			invOK(t, second.inviteErr)
			invEqual(t, second.row.DecisionConfirmedAt.IsZero(), false)
			settleContactCount(t, joins, 1)
			settleContactCount(t, pairs, 1)
		})
	}
}

func TestSettleAcceptanceInvalidRequestsNeverReadOrWriteAnOutcome(t *testing.T) {
	r, row := settleReady(t, peer.PairingAccepted)
	for _, request := range []peer.Settle{
		{JoinerOperationID: "", InviterOperationID: row.InviterOperationID, InvitationID: row.InvitationID, TranscriptID: row.TranscriptID},
		{JoinerOperationID: strings.Repeat("x", 129), InviterOperationID: row.InviterOperationID, InvitationID: row.InvitationID, TranscriptID: row.TranscriptID},
		{JoinerOperationID: row.OperationID, InviterOperationID: row.InviterOperationID, InvitationID: row.InvitationID, TranscriptID: bytes.Repeat([]byte{1}, 65)},
	} {
		frame, err := peer.EncodeSettle(request)
		if err != nil {
			continue
		} // Refusal at encoding is also fail-closed.
		frames := &settleScript{in: [][]byte{frame}}
		err = settleInviter(t, r).ServeSettle(context.Background(), frames, peer.Connection{PeerLeaf: r.bob.Fingerprint()})
		invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
		invEqual(t, len(frames.sent), 0)
	}
	for _, frame := range [][]byte{bytes.Repeat([]byte{1}, peer.MaxFrameBytes+1), []byte(`{"type":"operator_send"}`)} {
		frames := &settleScript{in: [][]byte{frame}}
		err := settleInviter(t, r).ServeSettle(context.Background(), frames, peer.Connection{PeerLeaf: r.bob.Fingerprint()})
		invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
		invEqual(t, len(frames.sent), 0)
	}
	invEqual(t, settleRequest(t, r).DeliveredAt.IsZero(), true)
}

func TestSettleAcceptanceInvalidDecisionAndReceiptValuesAreRefused(t *testing.T) {
	r, row := settleReady(t, peer.PairingAccepted)
	valid, err := peer.EncodeSettleReply(peer.SettleReply{Receipt: peer.ReceiptMatched, Decision: peer.PairingAccepted})
	invOK(t, err)
	badDecision := bytes.Replace(valid, []byte("accepted"), []byte("nonsense"), 1)
	frames := &settleScript{in: [][]byte{badDecision}}
	_, err = settleJoiner(t, r).Settle(context.Background(), frames, peer.Connection{PeerLeaf: r.alice.Fingerprint()}, row.OperationID)
	invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
	invEqual(t, settleStored(t, r, row.OperationID).State, peer.JoinAwaitingAcceptance)
	badReceipt, err := peer.EncodeSettleReply(peer.SettleReply{Receipt: 255, Decision: peer.PairingAccepted})
	invOK(t, err)
	_, err = peer.DecodeSettleReply(badReceipt)
	invEqual(t, errors.Is(err, peer.ErrPairingProtocol), true)
}
