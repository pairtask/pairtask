package messaging

import (
	"bytes"
	"context"
	"fmt"
)

// A control session (#276) carries at most one contact notice each way and
// nothing else; its ALPN fixes the frame set, so there is no Hello. The
// dialer sends its PairingEpoch and the dialed side answers with its own;
// different pairings end it on both sides with End. Then the dialer sends
// at most one ContactState, or End; the dialed side answers it with
// ContactStateStored and sends at most one ContactState of its own, or End;
// the dialer answers that, and the session is over. Any other frame is a
// protocol error.

// ControlStore is a control session's store. Every method checks the
// authority the store was admitted with inside its own transaction, and a
// fenced call ends the session.
type ControlStore interface {
	// PairingID is the pairing this side holds: nil for a legacy contact.
	PairingID(ctx context.Context) ([]byte, error)
	// PendingNotice is this side's oldest undelivered notice, nil if none.
	PendingNotice(ctx context.Context) (*ContactState, error)
	// AcceptNotice stores the peer's notice and says what to answer.
	AcceptNotice(ctx context.Context, n ContactState) (ControlAnswer, error)
	// NoticeStored records the peer's answer to this side's notice.
	NoticeStored(ctx context.Context, a ContactStateStored) error
}

// ControlAnswer is the answer to one notice. Close: storing it ended this
// side's authority (a remote unpaired), so the session sends only Ack and
// closes, with no further store call and no notice of its own.
type ControlAnswer struct {
	Ack   ContactStateStored
	Close bool
}

// ControlSession is one side of one control session with Contact.
type ControlSession struct {
	Frames  Frames
	Store   ControlStore
	Contact string
}

// ControlResult is what a control session did: Sent, this side's notice
// went out (whether or not its answer came back); Received, the peer's
// notice was stored; ClosedByUnpair, that notice ended the session.
type ControlResult struct{ Sent, Received, ClosedByUnpair bool }

// control is one running control session.
type control struct {
	ControlSession
	ctx    context.Context
	result ControlResult
}

// Dial runs the dialing side: its notice first, then the peer's.
func (s ControlSession) Dial(ctx context.Context) (ControlResult, error) {
	c := &control{ControlSession: s, ctx: ctx}
	err := c.dial()
	return c.result, err
}

// Serve runs the dialed side: the peer's notice first, then its own.
func (s ControlSession) Serve(ctx context.Context) (ControlResult, error) {
	c := &control{ControlSession: s, ctx: ctx}
	err := c.serve()
	return c.result, err
}

func (c *control) dial() error {
	if err := c.agree(true); err != nil {
		return err
	}
	if err := c.offer(); err != nil {
		return err
	}
	_, err := c.answer()
	return err
}

func (c *control) serve() error {
	if err := c.agree(false); err != nil {
		return err
	}
	closed, err := c.answer()
	if closed || err != nil {
		return err
	}
	return c.offer()
}

// agree swaps pairing IDs in this side's order; different ones end the
// session with End from both sides, before any notice is read or stored.
func (c *control) agree(dialer bool) error {
	own, err := c.Store.PairingID(c.ctx)
	if err != nil {
		return storeErr(err)
	}
	theirs, err := c.swap(own, dialer)
	if err != nil {
		return err
	}
	if bytes.Equal(own, theirs) {
		return nil
	}
	if err := c.endBoth(dialer); err != nil {
		return err
	}
	return ErrOtherPairing
}

// endBoth exchanges the two Ends of a mismatch in order, so neither side
// blocks sending while the other sends too: the dialer sends its End and
// reads the server's; the server reads the dialer's, then sends its own.
func (c *control) endBoth(dialer bool) error {
	if dialer {
		if err := c.sendEnd(); err != nil {
			return err
		}
		return c.receiveEnd()
	}
	if err := c.receiveEnd(); err != nil {
		return err
	}
	return c.sendEnd()
}

// receiveEnd reads the peer's End: any other frame is a protocol error.
func (c *control) receiveEnd() error {
	frame, err := c.Frames.Recv()
	if err != nil {
		return err
	}
	return DecodeEnd(frame)
}

// swap sends own and reads the peer's ID, the dialer first.
func (c *control) swap(own []byte, dialer bool) ([]byte, error) {
	if dialer {
		if err := c.sendEpoch(own); err != nil {
			return nil, err
		}
		return c.receiveEpoch()
	}
	theirs, err := c.receiveEpoch()
	if err != nil {
		return nil, err
	}
	return theirs, c.sendEpoch(own)
}

func (c *control) sendEpoch(id []byte) error {
	return c.send(EncodePairingEpoch(PairingEpoch{PairingID: id}))
}

// receiveEpoch reads the peer's ID: any other frame is a protocol error.
func (c *control) receiveEpoch() ([]byte, error) {
	frame, err := c.Frames.Recv()
	if err != nil {
		return nil, err
	}
	e, err := DecodePairingEpoch(frame)
	return e.PairingID, err
}

// offer sends this side's pending notice, or End, and records the answer.
func (c *control) offer() error {
	n, err := c.Store.PendingNotice(c.ctx)
	if err != nil {
		return storeErr(err)
	}
	if n == nil {
		return c.sendEnd()
	}
	if err := c.send(EncodeContactState(*n)); err != nil {
		return err
	}
	c.result.Sent = true
	return c.stored(*n)
}

// stored reads the answer to n, which must echo its pairing and generation.
func (c *control) stored(n ContactState) error {
	frame, err := c.Frames.Recv()
	if err != nil {
		return err
	}
	a, err := DecodeContactStateStored(frame)
	if err != nil {
		return err
	}
	if a.Gen != n.Gen || !bytes.Equal(a.PairingID, n.PairingID) {
		return fmt.Errorf("%w: answer for another notice", ErrProtocol)
	}
	return storeErr(c.Store.NoticeStored(c.ctx, a))
}

// answer reads the peer's notice or End, stores a notice and answers it;
// closed is a notice whose storing ended the session.
func (c *control) answer() (closed bool, err error) {
	frame, err := c.Frames.Recv()
	if err != nil {
		return false, err
	}
	if kind, _ := Kind(frame); kind == FrameEnd {
		return false, DecodeEnd(frame)
	}
	n, err := DecodeContactState(frame)
	if err != nil {
		return false, err
	}
	a, err := c.Store.AcceptNotice(c.ctx, n)
	if err != nil {
		return false, storeErr(err)
	}
	c.result.Received, c.result.ClosedByUnpair = true, a.Close
	return a.Close, c.send(EncodeContactStateStored(a.Ack))
}

func (c *control) sendEnd() error { return c.send(EncodeEnd()) }

func (c *control) send(frame []byte, err error) error {
	if err != nil {
		return err
	}
	return c.Frames.Send(frame)
}
