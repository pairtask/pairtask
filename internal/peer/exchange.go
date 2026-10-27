package peer

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// The code exchange (#178) is a PAKE run between the inviter, who holds the
// code, and the joiner, who typed it. Its wire format, key confirmation and
// transcript ID belong to the Exchanger, chosen separately; this package only
// orders it around durable state. No Exchanger ships yet, so redemption is
// refused before anything is charged or written.

// MaxChannelBindingBytes bounds the connection's channel binding.
const MaxChannelBindingBytes = 64

var (
	// ErrExchangeUnavailable: no code exchange is configured.
	ErrExchangeUnavailable = errors.New("peer: no code exchange available")
	// ErrExchangeFailed: the exchange did not verify the peer.
	ErrExchangeFailed = errors.New("peer: code exchange failed")
	// ErrInvalidBinding: an exchange binding or connection fact is missing,
	// oversized or not canonical.
	ErrInvalidBinding = errors.New("peer: invalid exchange binding")
	// ErrSelfPairing: the peer presented our own identity.
	ErrSelfPairing = errors.New("peer: cannot pair with this workspace itself")
	// ErrPeerMismatch: the exchange authenticated an identity other than the
	// certificate the connection presented.
	ErrPeerMismatch = errors.New("peer: exchange identity differs from the connection's")
)

// Frames carries one connection's pairing frames, each at most MaxFrameBytes.
type Frames interface {
	Send([]byte) error
	Recv() ([]byte, error)
}

// ExchangeBinding is everything the exchange must bind: both roles'
// fingerprints as presented on this connection, both operation IDs, the
// invitation, the joiner's name and the connection's channel binding.
type ExchangeBinding struct {
	InvitationID, JoinerOperationID, InviterOperationID string
	Inviter, Joiner                                     string
	JoinerName                                          string
	ChannelBinding                                      []byte
}

// Validate refuses a binding the exchange could not bind faithfully.
func (b ExchangeBinding) Validate() error {
	ids := []bool{validPairingID(b.InvitationID), validPairingID(b.JoinerOperationID), validPairingID(b.InviterOperationID)}
	if slices.Contains(ids, false) || !validPairingName(b.JoinerName) {
		return ErrInvalidBinding
	}
	if err := validPeers(b.Inviter, b.Joiner); err != nil {
		return err
	}
	return validChannelBinding(b.ChannelBinding)
}

func validPeers(inviter, joiner string) error {
	if ValidIdentityKey(inviter) != nil || ValidIdentityKey(joiner) != nil {
		return ErrInvalidBinding
	}
	if inviter == joiner {
		return ErrSelfPairing
	}
	return nil
}

func validChannelBinding(cb []byte) error {
	if len(cb) == 0 || len(cb) > MaxChannelBindingBytes {
		return ErrInvalidBinding
	}
	return nil
}

// ExchangeResult is what the exchange authenticated: the peer's identity and
// the exchange's own transcript ID.
type ExchangeResult struct {
	Peer         string
	TranscriptID []byte
}

// Exchanger runs the code exchange on one connection.
type Exchanger interface {
	// Join is the joiner's side. After verifying the inviter, and before any
	// byte of its own final confirmation, it must call persist and stop
	// unless persist succeeds.
	Join(ctx context.Context, f Frames, b ExchangeBinding, code []byte, persist func(context.Context, ExchangeResult) error) error
	// Invite is the inviter's side. It returns once it has verified the
	// joiner, sending nothing after that.
	Invite(ctx context.Context, f Frames, b ExchangeBinding, code []byte) (ExchangeResult, error)
}

// Connection is what the caller's connection established: the fingerprint
// of the leaf certificate the peer presented, and the channel binding.
type Connection struct {
	PeerLeaf       string
	ChannelBinding []byte
}

// check refuses a connection to ourselves or one with unusable facts.
func (c Connection) check(self string) error {
	if err := validPeers(c.PeerLeaf, self); err != nil {
		return err
	}
	return validChannelBinding(c.ChannelBinding)
}

// boundedFrames enforces MaxFrameBytes on every frame, the exchange's
// included.
type boundedFrames struct{ Frames }

func (b boundedFrames) Send(p []byte) error {
	if len(p) > MaxFrameBytes {
		return fmt.Errorf("%w: frame too long", ErrPairingProtocol)
	}
	return b.Frames.Send(p)
}

func (b boundedFrames) Recv() ([]byte, error) {
	p, err := b.Frames.Recv()
	if err == nil && len(p) > MaxFrameBytes {
		return nil, fmt.Errorf("%w: frame too long", ErrPairingProtocol)
	}
	return p, err
}
