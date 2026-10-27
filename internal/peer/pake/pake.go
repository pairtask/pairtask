// Package pake runs pairing's code exchange with CPace (#178):
// github.com/the-sarge/cpace v0.1.3, which implements
// draft-irtf-cfrg-cpace-21 over ristretto255 and SHA-512.
//
// EXPERIMENTAL: that library is an unaudited implementation of an active
// Internet-Draft. Using it here is an operator decision to integrate and test
// it, not a claim that it is production-ready.
//
// Every value both sides must agree on is bound into what CPace
// authenticates: the channel binding as the session ID, each side's
// role-prefixed fingerprint as its own and its peer's ID, and the profile,
// invitation, both operation IDs and the joiner's name as the context. A
// difference in any of them fails the exchange. Local associated data is
// unused, since CPace does not require both sides to agree on it.
package pake

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	cpace "github.com/the-sarge/cpace"

	"github.com/pairtask/pairtask/internal/peer"
)

// profile names this binding; the #182 spike's was v1.
const profile = "pairtask-pairing-v2"

const (
	inviterPrefix = "pairtask-inviter:"
	joinerPrefix  = "pairtask-joiner:"
)

type exchanger struct{}

// New returns the CPace exchanger.
func New() peer.Exchanger { return exchanger{} }

// input is one side's CPace input for a binding.
func input(b peer.ExchangeBinding, code []byte, joiner bool) cpace.Input {
	in := cpace.Input{
		Password:  code,
		SessionID: b.ChannelBinding,
		Context:   bindingContext(b),
		SelfID:    []byte(inviterPrefix + b.Inviter),
		PeerID:    []byte(joinerPrefix + b.Joiner),
	}
	if joiner {
		in.SelfID, in.PeerID = in.PeerID, in.SelfID
	}
	return in
}

// bindingContext is the profile, the invitation, both operation IDs and the
// joiner's name, each with a big-endian uint32 length.
func bindingContext(b peer.ExchangeBinding) []byte {
	var out []byte
	for _, field := range []string{profile, b.InvitationID, b.JoinerOperationID, b.InviterOperationID, b.JoinerName} {
		out = binary.BigEndian.AppendUint32(out, uint32(len(field)))
		out = append(out, field...)
	}
	return out
}

// ready refuses a cancelled context or a binding that can't be bound.
func ready(ctx context.Context, b peer.ExchangeBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.Validate()
}

// Join runs the initiator. Its final message leaves only after persist
// recorded what the exchange verified.
func (exchanger) Join(ctx context.Context, f peer.Frames, b peer.ExchangeBinding, code []byte, persist func(context.Context, peer.ExchangeResult) error) error {
	if err := ready(ctx, b); err != nil {
		return err
	}
	if persist == nil {
		return peer.ErrInvalidBinding
	}
	f = peer.BoundFrames(f)
	initiator, msgA, err := cpace.Start(input(b, code, true))
	if err != nil {
		return exchangeError(err)
	}
	defer initiator.Close()
	msgC, session, err := finishJoin(f, initiator, msgA)
	if err != nil {
		return err
	}
	defer session.Close()
	if err := persist(ctx, peer.ExchangeResult{Peer: b.Inviter, TranscriptID: session.TranscriptID()}); err != nil {
		return err
	}
	return f.Send(msgC)
}

// finishJoin sends message A and verifies the responder's message B.
func finishJoin(f peer.Frames, initiator *cpace.Initiator, msgA []byte) ([]byte, *cpace.Session, error) {
	if err := f.Send(msgA); err != nil {
		return nil, nil, err
	}
	msgB, err := f.Recv()
	if err != nil {
		return nil, nil, err
	}
	msgC, session, err := initiator.Finish(msgB)
	if err != nil {
		return nil, nil, exchangeError(err)
	}
	return msgC, session, nil
}

// Invite runs the responder and returns once it verified the joiner.
func (exchanger) Invite(ctx context.Context, f peer.Frames, b peer.ExchangeBinding, code []byte) (peer.ExchangeResult, error) {
	if err := ready(ctx, b); err != nil {
		return peer.ExchangeResult{}, err
	}
	f = peer.BoundFrames(f)
	msgA, err := f.Recv()
	if err != nil {
		return peer.ExchangeResult{}, err
	}
	responder, msgB, err := cpace.Respond(input(b, code, false), msgA)
	if err != nil {
		return peer.ExchangeResult{}, exchangeError(err)
	}
	defer responder.Close()
	if err := f.Send(msgB); err != nil {
		return peer.ExchangeResult{}, err
	}
	msgC, err := f.Recv()
	if err != nil {
		return peer.ExchangeResult{}, err
	}
	session, err := responder.Finish(msgC)
	if err != nil {
		return peer.ExchangeResult{}, exchangeError(err)
	}
	defer session.Close()
	return peer.ExchangeResult{Peer: b.Joiner, TranscriptID: session.TranscriptID()}, nil
}

// exchangeError marks a CPace refusal: a wrong code, a binding mismatch or
// a malformed message all fail verification alike.
func exchangeError(err error) error {
	return errors.Join(peer.ErrExchangeFailed, fmt.Errorf("cpace: %w", err))
}
