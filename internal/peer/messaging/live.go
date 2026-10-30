package messaging

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"
)

// A live link is a LiveVersion session kept open after its first round, so a
// message leaves the moment it is queued, on either side. Between rounds the
// link is idle:
//
//	dialer → dialed: Round (start one now) or Ping
//	dialed → dialer: Pending (I have something; once until the next Round) or Pong
//
// The dialed side answers Round with RoundAck and the four phases follow as
// in any session. The dialer skips a Pending or Pong that crossed its Round,
// so no stray frame is ever read inside a round. One goroutine reads the
// connection for the link's whole life and one writes, so an idle read is
// interrupted by closing the connection, partial frame or not.

// ErrRotate ends a link that has carried enough for the relays on its path,
// or lived long enough; the dialer reconnects at once. It isn't a failure.
var ErrRotate = errors.New("messaging: link rotated")

// ErrLinkDead is a link whose peer stopped answering.
var ErrLinkDead = errors.New("messaging: link heartbeat lost")

// ErrNotConfirmed ends a link whose contact is no longer confirmed here.
var ErrNotConfirmed = errors.New("messaging: contact no longer confirmed")

// LinkTiming bounds a link.
type LinkTiming struct {
	Round     time.Duration // one round's network work
	Grace     time.Duration // a round's store writes get this beyond Round
	Heartbeat time.Duration // the dialer pings this often while idle; the dialed side waits three
	Rotate    int64         // bytes either way after which the dialer reconnects before its next round
	MaxAge    time.Duration // and the age
}

// Link is one side of a live link. Session's Frames is not used: rounds read
// what the link's reader received and write through Open.
type Link struct {
	Session
	// Open is the connection's frames under ctx: its deadline applies, and
	// cancelling it interrupts a blocked send or receive.
	Open func(ctx context.Context) Frames
	// Wake carries a token whenever this side has something for the contact.
	Wake <-chan struct{}
	// Begin claims the contact for one round, or reports it busy; Confirmed
	// says whether the contact is still confirmed here. Either may be nil.
	// end gets the round's result and error: progress is recorded either
	// way, success only without an error.
	Begin     func() (end func(Result, error), ok bool)
	Confirmed func() bool
	// More says whether this side still has messages or receipts for the
	// contact: a round carries at most a session's worth, so another
	// follows at once. Nil means never.
	More   func() bool
	Timing LinkTiming
	// Carried is what the first round moved over the connection, either
	// way, counted towards Rotate.
	Carried struct{ Sent, Recv int64 }

	in         chan received
	again      chan struct{}
	sent, recv atomic.Int64
	started    time.Time
}

type received struct {
	frame []byte
	err   error
}

// RunDialer keeps the dialing side of a link until ctx ends, the peer goes
// quiet, the link must rotate, or a round fails.
func (l *Link) RunDialer(ctx context.Context) error {
	ctx, cancel := l.start(ctx)
	defer cancel()
	ping := time.NewTicker(l.Timing.Heartbeat)
	defer ping.Stop()
	missed := 0
	for {
		var err error
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.Wake:
			err = l.dialRound(ctx, &missed)
		case <-l.again:
			err = l.dialRound(ctx, &missed)
		case r := <-l.in:
			err = l.dialerIdle(ctx, r, &missed)
		case <-ping.C:
			err = l.ping(ctx, &missed)
		}
		if err != nil {
			return err
		}
	}
}

// dialerIdle handles what the dialed side sent between rounds.
func (l *Link) dialerIdle(ctx context.Context, r received, missed *int) error {
	kind, err := expectSignal(r, FramePending, FramePong)
	if err != nil {
		return err
	}
	if kind == FramePending {
		return l.dialRound(ctx, missed)
	}
	*missed = 0
	return nil
}

// expectSignal is the field-less live frame received, when it is one of
// kinds; anything else is a protocol error.
func expectSignal(r received, kinds ...byte) (byte, error) {
	if r.err != nil {
		return 0, r.err
	}
	kind, err := Kind(r.frame)
	if err != nil {
		return 0, err
	}
	if !slices.Contains(kinds, kind) {
		return 0, fmt.Errorf("%w: unexpected frame %d", ErrProtocol, kind)
	}
	return kind, DecodeSignal(r.frame, kind)
}

// ping sends a heartbeat, unless two went unanswered already; an idle link
// past its age rotates here rather than waiting for a round.
func (l *Link) ping(ctx context.Context, missed *int) error {
	if *missed >= 2 {
		return ErrLinkDead
	}
	if err := l.checkConfirmed(); err != nil {
		return err
	}
	if l.Now().Sub(l.started) > l.Timing.MaxAge {
		return ErrRotate
	}
	l.followUp() // retry what an earlier round left, refusals included
	*missed++
	return l.idleSignal(ctx, FramePing)
}

// dialRound starts and runs one round, unless the link must rotate first.
func (l *Link) dialRound(ctx context.Context, missed *int) error {
	if err := l.beforeRound(); err != nil {
		return err
	}
	end, ok := l.begin()
	if !ok {
		l.retry()
		return nil
	}
	roundCtx, cancel := context.WithTimeout(ctx, l.Timing.Round)
	defer cancel()
	if err := l.signal(roundCtx, FrameRound); err != nil {
		end(Result{}, err)
		return err
	}
	if err := l.awaitAck(roundCtx, missed); err != nil {
		end(Result{}, err)
		return err
	}
	r, err := l.round(roundCtx, true)
	end(r, err)
	if err == nil && progressed(r) {
		l.followUp()
	}
	return err
}

// progressed: a round stored, received or confirmed something. A round
// that only met refusals doesn't chain another; the heartbeat retries.
func progressed(r Result) bool {
	return r.Sent+r.Received+r.ReadsSent+r.ReadsStored > 0
}

// followUp asks for another round at once while this side has more.
func (l *Link) followUp() {
	if l.More != nil && l.More() {
		select {
		case l.again <- struct{}{}:
		default:
		}
	}
}

// awaitAck reads up to RoundAck, skipping a Pending or Pong that crossed
// the Round.
func (l *Link) awaitAck(ctx context.Context, missed *int) error {
	frames := l.frames(ctx)
	for {
		frame, err := frames.Recv()
		kind, err := expectSignal(received{frame, err}, FrameRoundAck, FramePong, FramePending)
		if err != nil || kind == FrameRoundAck {
			return err
		}
		if kind == FramePong {
			*missed = 0
		}
	}
}

// RunServer keeps the dialed side of a link until ctx ends, the dialer goes
// quiet, or a round fails.
func (l *Link) RunServer(ctx context.Context) error {
	ctx, cancel := l.start(ctx)
	defer cancel()
	quiet := time.NewTimer(3 * l.Timing.Heartbeat)
	defer quiet.Stop()
	pending := false
	for {
		var err error
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.Wake:
			err = l.offerPending(ctx, &pending)
		case <-l.again:
			err = l.offerPending(ctx, &pending)
		case r := <-l.in:
			quiet.Reset(3 * l.Timing.Heartbeat)
			err = l.serverIdle(ctx, r, &pending)
		case <-quiet.C:
			return ErrLinkDead
		}
		if err != nil {
			return err
		}
	}
}

// offerPending tells the dialer this side has something, once per round.
func (l *Link) offerPending(ctx context.Context, pending *bool) error {
	if *pending {
		return nil
	}
	*pending = true
	return l.idleSignal(ctx, FramePending)
}

// serverIdle handles what the dialer sent between rounds.
func (l *Link) serverIdle(ctx context.Context, r received, pending *bool) error {
	kind, err := expectSignal(r, FramePing, FrameRound)
	if err != nil {
		return err
	}
	if kind == FramePing {
		return l.answerPing(ctx, pending)
	}
	*pending = false
	done, err := l.serveRound(ctx)
	if err != nil || !progressed(done) {
		return err
	}
	return l.offerLeft(ctx, pending)
}

// answerPing answers the dialer's heartbeat, then offers what an earlier
// round left here, refusals included: a retry once per heartbeat.
func (l *Link) answerPing(ctx context.Context, pending *bool) error {
	if err := l.checkConfirmed(); err != nil {
		return err
	}
	if err := l.idleSignal(ctx, FramePong); err != nil {
		return err
	}
	return l.offerLeft(ctx, pending)
}

// offerLeft tells the dialer this side has something, when it has.
func (l *Link) offerLeft(ctx context.Context, pending *bool) error {
	if l.More == nil || !l.More() {
		return nil
	}
	return l.offerPending(ctx, pending)
}

// serveRound claims the contact, waiting while a replaced link or another
// session still holds it, then acknowledges the Round and runs it. A guard
// never freed within the round's budget ends the link; the dialer
// reconnects.
func (l *Link) serveRound(ctx context.Context) (Result, error) {
	if err := l.checkConfirmed(); err != nil {
		return Result{}, err
	}
	roundCtx, cancel := context.WithTimeout(ctx, l.Timing.Round)
	defer cancel()
	end, err := l.awaitBegin(roundCtx)
	if err != nil {
		return Result{}, err
	}
	if err := l.signal(roundCtx, FrameRoundAck); err != nil {
		end(Result{}, err)
		return Result{}, err
	}
	r, err := l.round(roundCtx, false)
	end(r, err)
	return r, err
}

// awaitBegin claims the contact for a round, retrying until ctx ends.
func (l *Link) awaitBegin(ctx context.Context) (func(Result, error), error) {
	for {
		if end, ok := l.begin(); ok {
			return end, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("messaging: contact busy: %w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// start readies the link and starts its reader; cancel stops both.
func (l *Link) start(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	l.in, l.again = make(chan received, 1), make(chan struct{}, 1)
	l.started = l.Now()
	l.Store = sessionStore{l.Store}
	l.sent.Store(l.Carried.Sent)
	l.recv.Store(l.Carried.Recv)
	l.followUp() // the first round may have left more than it carried
	go l.read(ctx)
	return ctx, cancel
}

// read is the link's only reader, for its whole life; it stops at the first
// error, which it hands on, or when ctx ends.
func (l *Link) read(ctx context.Context) {
	frames := l.Open(ctx)
	for {
		frame, err := frames.Recv()
		if err == nil {
			l.recv.Add(int64(len(frame) + 4))
		}
		select {
		case l.in <- received{frame, err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// round runs the four phases over the link, with its own store deadline.
func (l *Link) round(ctx context.Context, dialer bool) (Result, error) {
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.Timing.Round+l.Timing.Grace)
	defer cancel()
	s := l.Session
	s.Frames = l.frames(ctx)
	return s.phases(storeCtx, dialer)
}

// beforeRound ends a link that must rotate or whose contact went away.
func (l *Link) beforeRound() error {
	if err := l.checkConfirmed(); err != nil {
		return err
	}
	if l.sent.Load() > l.Timing.Rotate || l.recv.Load() > l.Timing.Rotate {
		return ErrRotate
	}
	if l.Now().Sub(l.started) > l.Timing.MaxAge {
		return ErrRotate
	}
	return nil
}

func (l *Link) checkConfirmed() error {
	if l.Confirmed != nil && !l.Confirmed() {
		return ErrNotConfirmed
	}
	return nil
}

func (l *Link) begin() (func(Result, error), bool) {
	if l.Begin == nil {
		return func(Result, error) {}, true
	}
	end, ok := l.Begin()
	if !ok {
		return func(Result, error) {}, false
	}
	return end, true
}

// retry tries a round again shortly, when the contact was busy.
func (l *Link) retry() {
	time.AfterFunc(time.Second, func() {
		select {
		case l.again <- struct{}{}:
		default:
		}
	})
}

// idleSignal sends a field-less live frame between rounds, within a round's
// budget, so a peer that stopped reading can't hold the only writer.
func (l *Link) idleSignal(ctx context.Context, kind byte) error {
	ctx, cancel := context.WithTimeout(ctx, l.Timing.Round)
	defer cancel()
	return l.signal(ctx, kind)
}

// signal sends a field-less live frame.
func (l *Link) signal(ctx context.Context, kind byte) error {
	frame, err := EncodeSignal(kind)
	if err != nil {
		return err
	}
	return l.frames(ctx).Send(frame)
}

func (l *Link) frames(ctx context.Context) Frames { return linkFrames{l: l, ctx: ctx} }

// Counted is frames that count their bytes each way, as a link does, so a
// first round's traffic counts towards rotation (Link.Carried).
type Counted struct {
	Frames               Frames
	SentBytes, RecvBytes int64
}

func (c *Counted) Send(p []byte) error {
	c.SentBytes += int64(len(p) + 4)
	return c.Frames.Send(p)
}

func (c *Counted) Recv() ([]byte, error) {
	p, err := c.Frames.Recv()
	if err == nil {
		c.RecvBytes += int64(len(p) + 4)
	}
	return p, err
}

// linkFrames reads what the link's reader received and writes through Open,
// counting both ways for rotation.
type linkFrames struct {
	l   *Link
	ctx context.Context
}

func (f linkFrames) Send(p []byte) error {
	f.l.sent.Add(int64(len(p) + 4))
	return f.l.Open(f.ctx).Send(p)
}

func (f linkFrames) Recv() ([]byte, error) {
	select {
	case r := <-f.l.in:
		return r.frame, r.err
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}
