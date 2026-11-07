package messaging

import "context"

// Compile-only seam for the frozen #276 control-session tests; no control
// behaviour yet.

// ControlStore is a control session's store; every method fenced.
type ControlStore interface {
	PairingID(ctx context.Context) ([]byte, error)
	PendingNotice(ctx context.Context) (*ContactState, error)
	AcceptNotice(ctx context.Context, n ContactState) (ControlAnswer, error)
	NoticeStored(ctx context.Context, a ContactStateStored) error
}

// ControlAnswer is what to send for a notice; Close: send only Ack, then close.
type ControlAnswer struct {
	Ack   ContactStateStored
	Close bool
}

// ControlSession is one side of one control session.
type ControlSession struct {
	Frames  Frames
	Store   ControlStore
	Contact string
}

// ControlResult is what a control session did.
type ControlResult struct{ Sent, Received, ClosedByUnpair bool }

// Dial runs the dialing side (seam: not implemented).
func (ControlSession) Dial(context.Context) (ControlResult, error) {
	return ControlResult{}, ErrProtocol
}

// Serve runs the served side (seam: not implemented).
func (ControlSession) Serve(context.Context) (ControlResult, error) {
	return ControlResult{}, ErrProtocol
}
