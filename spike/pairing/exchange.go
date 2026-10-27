package pairing

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/the-sarge/cpace"
)

// maxFrame bounds one pairing frame; CPace messages are far smaller.
const maxFrame = 1024

// confirmed is the inviter's last frame. It travels over the TLS connection
// the exchange just bound, after the inviter verified the joiner. It
// acknowledges the pairing confirmation only, never contact acceptance.
var confirmed = []byte("confirmed")

// Frames carries the exchange's messages over the first-contact connection.
type Frames interface {
	Send([]byte) error
	Recv() ([]byte, error)
}

// Result is a confirmed pairing as one side sees it.
type Result struct {
	Peer         [32]byte // the peer's certificate fingerprint, now verified
	TranscriptID []byte   // CPace's transcript identifier
	// InviterConfirmed is the joiner's view: the inviter's final frame
	// arrived, so the inviter verified the joiner too. It acknowledges the
	// pairing only; contact acceptance is a separate, explicit operator step.
	InviterConfirmed bool
}

var (
	// ErrUnconfirmed means the exchange didn't complete: wrong code, a
	// different connection, invitation or identity, a broken message, or a
	// peer that hung up first (it detects a mismatch before we do). Nothing
	// may be trusted or recorded.
	ErrUnconfirmed = errors.New("pairing: not confirmed")
	// ErrOutcomeUnknown means the joiner confirmed the inviter, but the
	// inviter's final frame didn't arrive, so the joiner can't know whether
	// the inviter verified it.
	ErrOutcomeUnknown = errors.New("pairing: confirmed, inviter's outcome unknown")
)

// unconfirmed marks a failure before this side verified its peer.
func unconfirmed(err error) error { return fmt.Errorf("%w: %w", ErrUnconfirmed, err) }

func (b Binding) input(role Role, code []byte) cpace.Input {
	in := cpace.Input{Password: code, Context: b.context(), SessionID: b.EKM}
	in.SelfID, in.PeerID = identity(Inviter, b.InviterFP), identity(Joiner, b.JoinerFP)
	if role == Joiner {
		in.SelfID, in.PeerID = in.PeerID, in.SelfID
	}
	return in
}

// Join runs the joiner's side. It returns a Result only after verifying the
// inviter's confirmation; with ErrOutcomeUnknown the Result is still filled.
func Join(f Frames, b Binding, code []byte) (Result, error) {
	initiator, msgA, err := cpace.Start(b.input(Joiner, code))
	if err != nil {
		return Result{}, unconfirmed(err)
	}
	defer initiator.Close()
	if err := f.Send(msgA); err != nil {
		return Result{}, unconfirmed(err)
	}
	msgB, err := f.Recv()
	if err != nil {
		return Result{}, unconfirmed(err)
	}
	msgC, session, err := initiator.Finish(msgB)
	if err != nil {
		return Result{}, unconfirmed(err)
	}
	defer session.Close()
	res := Result{Peer: b.InviterFP, TranscriptID: session.TranscriptID()}
	if err := f.Send(msgC); err != nil {
		return res, fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}
	last, err := f.Recv()
	if err != nil || string(last) != string(confirmed) {
		return res, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	res.InviterConfirmed = true
	return res, nil
}

// Invite runs the inviter's side. It returns a Result only after verifying the
// joiner's confirmation, and then tells the joiner.
func Invite(f Frames, b Binding, code []byte) (Result, error) {
	msgA, err := f.Recv()
	if err != nil {
		return Result{}, unconfirmed(err)
	}
	responder, msgB, err := cpace.Respond(b.input(Inviter, code), msgA)
	if err != nil {
		return Result{}, unconfirmed(err)
	}
	defer responder.Close()
	if err := f.Send(msgB); err != nil {
		return Result{}, unconfirmed(err)
	}
	msgC, err := f.Recv()
	if err != nil {
		return Result{}, unconfirmed(err)
	}
	session, err := responder.Finish(msgC)
	if err != nil {
		return Result{}, unconfirmed(err)
	}
	defer session.Close()
	res := Result{Peer: b.JoinerFP, TranscriptID: session.TranscriptID()}
	return res, f.Send(confirmed)
}

// StreamFrames frames messages over a byte stream with a 4-byte big-endian
// length, refusing anything above maxFrame.
type StreamFrames struct{ RW io.ReadWriter }

func (s StreamFrames) Send(p []byte) error {
	if len(p) > maxFrame {
		return fmt.Errorf("pairing: frame of %d bytes exceeds %d", len(p), maxFrame)
	}
	buf := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(p)), uint32(len(p)))
	_, err := s.RW.Write(append(buf, p...))
	return err
}

func (s StreamFrames) Recv() ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(s.RW, size[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n > maxFrame {
		return nil, fmt.Errorf("pairing: frame of %d bytes exceeds %d", n, maxFrame)
	}
	p := make([]byte, n)
	_, err := io.ReadFull(s.RW, p)
	return p, err
}
