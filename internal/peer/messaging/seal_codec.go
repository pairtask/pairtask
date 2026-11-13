package messaging

import (
	"context"
	"fmt"
)

// Seals (#115), version 10: a message travels with its sender's seal, the
// signed header internal/signing checks. The frame wraps the one the
// message would otherwise go as, so plain, routed and review messages carry
// a seal alike; a peer before version 10 gets the inner frame alone, and
// the sender records that the seal didn't travel.

const (
	// SealVersion is the first version that carries seals.
	SealVersion byte = 10
	// FrameSealed is a message frame and its seal; an empty seal is an
	// unsigned message.
	FrameSealed byte = 30
	// MaxSealBytes bounds one seal.
	MaxSealBytes = 16 << 10
)

var sealedShape = shape{FrameSealed, 2}

// EncodeSealed wraps a message frame with its seal.
func EncodeSealed(inner, seal []byte) ([]byte, error) {
	if len(seal) > MaxSealBytes {
		return nil, fmt.Errorf("%w: seal too long", ErrProtocol)
	}
	return encode(FrameSealed, inner, seal)
}

// DecodeSealed is a sealed frame's inner frame and its seal; only a
// message frame (plain, routed or a review request) is ever sealed.
func DecodeSealed(frame []byte) (inner, seal []byte, err error) {
	f, err := decode(frame, sealedShape)
	if err != nil {
		return nil, nil, err
	}
	if len(f[1]) > MaxSealBytes {
		return nil, nil, fmt.Errorf("%w: seal too long", ErrProtocol)
	}
	if !wellFormedMessage(f[0]) {
		return nil, nil, fmt.Errorf("%w: sealed frame", ErrProtocol)
	}
	return f[0], f[1], nil
}

// sealable are the frames a seal may wrap, messages and never control,
// each with its own decoder.
var sealable = map[byte]func([]byte) (Message, error){
	FrameMessage: DecodeMessage, FrameRouted: DecodeRouted, FrameReviewAsk: DecodeReviewAsk,
}

// wellFormedMessage: frame is a message frame that its own decoder reads.
func wellFormedMessage(frame []byte) bool {
	kind, err := Kind(frame)
	if err != nil {
		return false
	}
	decode, ok := sealable[kind]
	if !ok {
		return false
	}
	_, err = decode(frame)
	return err == nil
}

// SealStore keeps messages' seals; a store with one speaks version 10.
// Queue and Accept keep a Message's Seal, and Offer returns it.
type SealStore interface {
	// SealDowngraded records that an outbound message's seal didn't
	// travel: the peer's version predates seals.
	SealDowngraded(ctx context.Context, contact, id string) error
}

// sealFrame is how m's frame travels in this session: wrapped with its
// seal from SealVersion on; before, the inner frame alone, and a seal
// that couldn't travel is recorded.
func (s Session) sealFrame(ctx context.Context, m Message, inner []byte) ([]byte, error) {
	if s.Version >= SealVersion {
		return EncodeSealed(inner, m.Seal)
	}
	if st, ok := s.Store.(SealStore); ok && len(m.Seal) > 0 {
		if err := st.SealDowngraded(ctx, s.Contact, m.ID); err != nil {
			return nil, err
		}
	}
	return inner, nil
}

// decodeSealed reads a sealed message: its inner message, with the seal.
func (s Session) decodeSealed(frame []byte) (Message, error) {
	inner, seal, err := DecodeSealed(frame)
	if err != nil {
		return Message{}, err
	}
	kind, _ := Kind(inner)
	m, err := s.decodeMessage(kind, inner)
	m.Seal = seal
	return m, err
}

// sealVersionFor is the highest version a consent-capable store speaks:
// SealVersion with a seal store, else PairingVersion.
func sealVersionFor(st Store) byte {
	if _, ok := st.(SealStore); ok {
		return MaxVersion
	}
	return PairingVersion
}

// sealSessionStore is a session's view of a store that keeps seals: its
// downgrade record goes through, failures marked as the store's.
type sealSessionStore struct {
	consentSessionStore
	ss SealStore
}

func (s sealSessionStore) SealDowngraded(ctx context.Context, contact, id string) error {
	return storeErr(s.ss.SealDowngraded(ctx, contact, id))
}
