package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// A session is lockstep, one frame then its answer, in four phases after
// the hellos: the dialing side's messages, the other side's messages, then
// each side's read receipts the same way. A side stores what it receives
// before it answers, and records an answer before it sends the next frame,
// so a session lost anywhere leaves only steps that are safe to repeat.

// Frames is a lockstep frame stream: one sender and one receiver at a time.
type Frames interface {
	Send([]byte) error
	Recv() ([]byte, error)
}

// ErrUnsupported is a session with no common protocol version.
var ErrUnsupported = errors.New("messaging: no common protocol version")

// Result is what one session moved.
type Result struct {
	Sent, Received         int // messages answered Stored, either way
	Refused                int // this side's messages the peer refused, recorded
	ReadsSent, ReadsStored int // read receipts confirmed and received
}

// Moved reports durable progress: anything stored, refused or confirmed,
// even in a session that then failed.
func (r Result) Moved() bool {
	return r.Sent+r.Received+r.Refused+r.ReadsSent+r.ReadsStored > 0
}

// Session is one side of one session with one contact.
type Session struct {
	Frames  Frames
	Store   Store
	Contact string
	Now     func() time.Time
}

// Dial runs the dialing side: its hello, its messages, the peer's, then the
// receipts the same way.
func (s Session) Dial(ctx context.Context) (Result, error) {
	s.Store = sessionStore{s.Store}
	if err := s.offerHello(); err != nil {
		return Result{}, err
	}
	return s.phases(ctx, true)
}

// Serve runs the side that was dialed.
func (s Session) Serve(ctx context.Context) (Result, error) {
	s.Store = sessionStore{s.Store}
	if err := s.answerHello(); err != nil {
		return Result{}, err
	}
	return s.phases(ctx, false)
}

// phases runs the four phases in order; dialer sends in the first and third.
func (s Session) phases(ctx context.Context, dialer bool) (Result, error) {
	var r Result
	steps := []func(context.Context, *Result) error{s.sendMessages, s.receiveMessages, s.sendReads, s.receiveReads}
	if !dialer {
		steps = []func(context.Context, *Result) error{s.receiveMessages, s.sendMessages, s.receiveReads, s.sendReads}
	}
	for _, step := range steps {
		if err := step(ctx, &r); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (s Session) offerHello() error {
	if err := s.send(EncodeHello(Hello{Min: MinVersion, Max: MaxVersion})); err != nil {
		return err
	}
	frame, err := s.Frames.Recv()
	if err != nil {
		return err
	}
	if kind, _ := Kind(frame); kind == FrameUnsupported {
		if _, err := DecodeUnsupported(frame); err != nil {
			return err // a malformed answer is a protocol error, not a version one
		}
		return ErrUnsupported
	}
	h, err := DecodeHello(frame)
	if err != nil {
		return err
	}
	return common(h)
}

func (s Session) answerHello() error {
	frame, err := s.Frames.Recv()
	if err != nil {
		return err
	}
	h, err := DecodeHello(frame)
	if err != nil {
		return err
	}
	if err := common(h); err != nil {
		s.send(EncodeUnsupported(Unsupported{Min: MinVersion, Max: MaxVersion}))
		return err
	}
	return s.send(EncodeHello(Hello{Min: MinVersion, Max: MaxVersion}))
}

// common requires a version both sides speak.
func common(h Hello) error {
	if h.Max < MinVersion || h.Min > MaxVersion {
		return ErrUnsupported
	}
	return nil
}

func (s Session) send(frame []byte, err error) error {
	if err != nil {
		return err
	}
	return s.Frames.Send(frame)
}

// sendMessages offers this side's oldest queued messages and records each
// answer before sending the next.
func (s Session) sendMessages(ctx context.Context, r *Result) error {
	rows, err := s.Store.Offer(ctx, s.Contact, s.Now(), SessionMessages, SessionBytes)
	if err != nil {
		return err
	}
	for _, row := range rows {
		stored, err := s.sendMessage(ctx, row.Message)
		if err != nil {
			return err
		}
		if stored {
			r.Sent++
		} else {
			r.Refused++
		}
	}
	return s.send(EncodeEnd())
}

func (s Session) sendMessage(ctx context.Context, m Message) (bool, error) {
	if err := s.send(EncodeMessage(m)); err != nil {
		return false, err
	}
	frame, err := s.Frames.Recv()
	if err != nil {
		return false, err
	}
	if kind, _ := Kind(frame); kind == FrameRefused {
		return false, s.recordRefusal(ctx, m.ID, frame)
	}
	ack, err := DecodeStored(frame)
	if err != nil {
		return false, err
	}
	if ack.ID != m.ID {
		return false, fmt.Errorf("%w: answer for another message", ErrProtocol)
	}
	return true, s.Store.Delivered(ctx, s.Contact, m.ID, s.Now())
}

func (s Session) recordRefusal(ctx context.Context, id string, frame []byte) error {
	refusal, err := DecodeRefused(frame)
	if err != nil {
		return err
	}
	if refusal.ID != id {
		return fmt.Errorf("%w: answer for another message", ErrProtocol)
	}
	return s.Store.Refused(ctx, s.Contact, id, refusal.Refusal, s.Now())
}

// receiveMessages stores the peer's messages and answers each one, within
// the session bounds, until its End.
func (s Session) receiveMessages(ctx context.Context, r *Result) error {
	count, bytes := 0, 0
	for {
		frame, err := s.Frames.Recv()
		if err != nil {
			return err
		}
		if kind, _ := Kind(frame); kind == FrameEnd {
			return DecodeEnd(frame)
		}
		m, err := DecodeMessage(frame)
		if err != nil {
			return err
		}
		if count, bytes = count+1, bytes+len(m.Text); count > SessionMessages || bytes > SessionBytes {
			return fmt.Errorf("%w: more than a session carries", ErrProtocol)
		}
		if err := s.answerMessage(ctx, m, r); err != nil {
			return err
		}
	}
}

// answerMessage stores, then answers; a store failure ends the session
// without an answer, so the peer offers the message again.
func (s Session) answerMessage(ctx context.Context, m Message, r *Result) error {
	answer, err := s.Store.Accept(ctx, s.Contact, m, s.Now())
	if err != nil {
		return err
	}
	if !answer.Stored {
		return s.send(EncodeRefused(Refused{ID: m.ID, Refusal: answer.Refusal}))
	}
	r.Received++
	return s.send(EncodeStored(Stored{ID: m.ID}))
}

// sendReads sends this side's pending read receipts, confirming each on its
// answer.
func (s Session) sendReads(ctx context.Context, r *Result) error {
	ids, err := s.Store.PendingReads(ctx, s.Contact, SessionMessages)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.sendRead(ctx, id); err != nil {
			return err
		}
		r.ReadsSent++
	}
	return s.send(EncodeEnd())
}

func (s Session) sendRead(ctx context.Context, id string) error {
	if err := s.send(EncodeRead(Read{ID: id})); err != nil {
		return err
	}
	frame, err := s.Frames.Recv()
	if err != nil {
		return err
	}
	ack, err := DecodeReadStored(frame)
	if err != nil {
		return err
	}
	if ack.ID != id {
		return fmt.Errorf("%w: answer for another receipt", ErrProtocol)
	}
	return s.Store.ReadConfirmed(ctx, s.Contact, id)
}

// receiveReads records the peer's receipts, answering each, until its End.
func (s Session) receiveReads(ctx context.Context, r *Result) error {
	for count := 0; ; count++ {
		frame, err := s.Frames.Recv()
		if err != nil {
			return err
		}
		if kind, _ := Kind(frame); kind == FrameEnd {
			return DecodeEnd(frame)
		}
		if count >= SessionMessages {
			return fmt.Errorf("%w: more receipts than a session carries", ErrProtocol)
		}
		read, err := DecodeRead(frame)
		if err != nil {
			return err
		}
		if err := s.Store.PeerRead(ctx, s.Contact, read.ID, s.Now()); err != nil {
			return err
		}
		r.ReadsStored++
		if err := s.send(EncodeReadStored(ReadStored{ID: read.ID})); err != nil {
			return err
		}
	}
}
