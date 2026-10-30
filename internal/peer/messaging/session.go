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
	// Review outcomes settled by the peer's answer, and taken from it.
	OutcomesSent, OutcomesStored int
}

// Moved reports durable progress: anything stored, refused or confirmed,
// even in a session that then failed.
func (r Result) Moved() bool {
	return r.Sent+r.Received+r.Refused+r.ReadsSent+r.ReadsStored+r.OutcomesSent+r.OutcomesStored > 0
}

// Session is one side of one session with one contact. Version is the one
// both sides agreed; OpenDial and OpenServe set it for their own phases,
// and a link's rounds carry it over.
type Session struct {
	Frames  Frames
	Store   Store
	Contact string
	Now     func() time.Time
	Version byte
}

// maxVersion is the highest version this side speaks with its store: a
// store without outcomes stops at review requests, one without routing at
// the live version.
func (s Session) maxVersion() byte {
	switch s.Store.(type) {
	case OutcomeStore:
		return MaxVersion
	case RoutingStore:
		return ReviewVersion
	}
	return LiveVersion
}

// routing is the store's routing half, when the agreed version uses it.
func (s Session) routing() (RoutingStore, bool) {
	if s.Version < RoutingVersion {
		return nil, false
	}
	r, ok := s.Store.(RoutingStore)
	return r, ok
}

// Dial runs the dialing side: its hello, its messages, the peer's, then the
// receipts the same way.
func (s Session) Dial(ctx context.Context) (Result, error) {
	r, _, err := s.OpenDial(ctx)
	return r, err
}

// Serve runs the side that was dialed.
func (s Session) Serve(ctx context.Context) (Result, error) {
	r, _, err := s.OpenServe(ctx)
	return r, err
}

// OpenDial is Dial, also reporting the version both sides speak: at
// LiveVersion the session may stay open (OfferLive).
func (s Session) OpenDial(ctx context.Context) (Result, byte, error) {
	max := s.maxVersion()
	s.Store = wrapStore(s.Store)
	version, err := s.offerHello(max)
	if err != nil {
		return Result{}, 0, err
	}
	s.Version = version
	r, err := s.phases(ctx, true)
	return r, version, err
}

// OpenServe is Serve, also reporting the version both sides speak.
func (s Session) OpenServe(ctx context.Context) (Result, byte, error) {
	max := s.maxVersion()
	s.Store = wrapStore(s.Store)
	version, err := s.answerHello(max)
	if err != nil {
		return Result{}, 0, err
	}
	s.Version = version
	r, err := s.phases(ctx, false)
	return r, version, err
}

// OfferLive, after a LiveVersion first round, says whether the dialing side
// wants to stay connected, and reports whether both will.
func (s Session) OfferLive(want bool) (bool, error) {
	if err := s.send(EncodeLive(want)); err != nil {
		return false, err
	}
	frame, err := s.Frames.Recv()
	if err != nil {
		return false, err
	}
	on, err := DecodeLive(frame)
	return want && on, err
}

// AnswerLive is the dialed side's half: it stays only if both can.
func (s Session) AnswerLive(can bool) (bool, error) {
	frame, err := s.Frames.Recv()
	if err != nil {
		return false, err
	}
	want, err := DecodeLive(frame)
	if err != nil {
		return false, err
	}
	on := want && can
	return on, s.send(EncodeLive(on))
}

// phase is one side of a phase: sending or receiving.
type phase func(context.Context, *Result) error

// phases runs the phases in order, each a pair the dialer opens: messages,
// read receipts, and from RoutingVersion on agent acknowledgments and the
// directories.
func (s Session) phases(ctx context.Context, dialer bool) (Result, error) {
	var r Result
	for _, step := range s.steps(dialer) {
		if err := step(ctx, &r); err != nil {
			return r, err
		}
	}
	return r, nil
}

// steps are the phases in this side's order: in each pair, the dialer
// sends first.
func (s Session) steps(dialer bool) []phase {
	pairs := [][2]phase{{s.sendMessages, s.receiveMessages}, {s.sendReads, s.receiveReads}}
	if _, ok := s.routing(); ok {
		pairs = append(pairs, [2]phase{s.sendAgentReads, s.receiveAgentReads}, [2]phase{s.sendDirectory, s.receiveDirectory})
	}
	if _, ok := s.outcomes(); ok {
		pairs = append(pairs, [2]phase{s.sendOutcomes, s.receiveOutcomes})
	}
	steps := make([]phase, 0, 2*len(pairs))
	for _, pair := range pairs {
		if !dialer {
			pair[0], pair[1] = pair[1], pair[0]
		}
		steps = append(steps, pair[0], pair[1])
	}
	return steps
}

func (s Session) offerHello(max byte) (byte, error) {
	if err := s.send(EncodeHello(Hello{Min: MinVersion, Max: max})); err != nil {
		return 0, err
	}
	frame, err := s.Frames.Recv()
	if err != nil {
		return 0, err
	}
	if kind, _ := Kind(frame); kind == FrameUnsupported {
		if _, err := DecodeUnsupported(frame); err != nil {
			return 0, err // a malformed answer is a protocol error, not a version one
		}
		return 0, ErrUnsupported
	}
	h, err := DecodeHello(frame)
	if err != nil {
		return 0, err
	}
	return common(h, max)
}

func (s Session) answerHello(max byte) (byte, error) {
	frame, err := s.Frames.Recv()
	if err != nil {
		return 0, err
	}
	h, err := DecodeHello(frame)
	if err != nil {
		return 0, err
	}
	version, err := common(h, max)
	if err != nil {
		s.send(EncodeUnsupported(Unsupported{Min: MinVersion, Max: max}))
		return 0, err
	}
	return version, s.send(EncodeHello(Hello{Min: MinVersion, Max: max}))
}

// common is the highest version both sides speak.
func common(h Hello, max byte) (byte, error) {
	if h.Max < MinVersion || h.Min > max {
		return 0, ErrUnsupported
	}
	return min(h.Max, max), nil
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
	rows, err := s.offer(ctx)
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

// offer is this session's messages to send: routed ones only when the
// agreed version carries them.
func (s Session) offer(ctx context.Context) ([]Row, error) {
	l := SessionLimit
	l.Version = s.Version
	return s.Store.Offer(ctx, s.Contact, s.Now(), l)
}

func (s Session) sendMessage(ctx context.Context, m Message) (bool, error) {
	encode := EncodeMessage
	switch {
	case m.Review != nil:
		encode = EncodeReviewAsk
	case m.Routed():
		encode = EncodeRouted
	}
	if err := s.send(encode(m)); err != nil {
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
	return s.Store.Refused(ctx, s.Contact, refusal)
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
		kind, _ := Kind(frame)
		if kind == FrameEnd {
			return DecodeEnd(frame)
		}
		m, err := s.decodeMessage(kind, frame)
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

// decodeMessage reads a Message, or a routed one at RoutingVersion.
func (s Session) decodeMessage(kind byte, frame []byte) (Message, error) {
	if decode := frameDecoders[kind]; decode.version != 0 && s.Version >= decode.version {
		return decode.decode(frame)
	}
	return DecodeMessage(frame)
}

// frameDecoders are the message frames of later versions, from the version
// that carries each.
var frameDecoders = map[byte]struct {
	version byte
	decode  func([]byte) (Message, error)
}{
	FrameRouted:    {RoutingVersion, DecodeRouted},
	FrameReviewAsk: {ReviewVersion, DecodeReviewAsk},
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
