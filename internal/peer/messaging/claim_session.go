package messaging

import (
	"context"
	"fmt"
	"time"
)

// ClaimStore is what a ClaimVersion session needs besides Store: the claim
// states this side owes the senders of messages its agents claimed, and
// the claims it is told of messages it sent (#419).
type ClaimStore interface {
	Store
	// PendingClaimNotices is up to max claim states still to send contact.
	PendingClaimNotices(ctx context.Context, contact string, max int) ([]ClaimNotice, error)
	// ClaimNoticeAnswered records the contact's answer to the state it was
	// sent: kept there, or dropped for good.
	ClaimNoticeAnswered(ctx context.Context, contact string, r ClaimReply) error
	// AcceptClaimNotice takes the contact's notice of a claim on a message
	// this side sent it.
	AcceptClaimNotice(ctx context.Context, contact string, n ClaimNotice, at time.Time) (ClaimAnswer, error)
}

// ClaimAnswer is how a notice was taken: Stored, or dropped for Reason.
type ClaimAnswer struct {
	Stored bool
	Reason string
}

// claims is the store's claim half, when the agreed version uses it.
func (s Session) claims() (ClaimStore, bool) {
	if s.Version < ClaimVersion {
		return nil, false
	}
	c, ok := s.Store.(ClaimStore)
	return c, ok
}

// sendClaimNotices sends this side's pending claim states, each answered
// before the next. An answer settles its notice either way: a dropped one
// is never offered again.
func (s Session) sendClaimNotices(ctx context.Context, r *Result) error {
	cs, _ := s.claims()
	pending, err := cs.PendingClaimNotices(ctx, s.Contact, SessionMessages)
	if err != nil {
		return err
	}
	for _, n := range pending {
		if err := s.sendClaimNotice(ctx, cs, n); err != nil {
			return err
		}
		r.ClaimNoticesSent++
	}
	return s.send(EncodeEnd())
}

func (s Session) sendClaimNotice(ctx context.Context, cs ClaimStore, n ClaimNotice) error {
	if err := s.send(EncodeClaimNotice(n)); err != nil {
		return err
	}
	frame, err := s.Frames.Recv()
	if err != nil {
		return err
	}
	reply, err := DecodeClaimReply(frame)
	if err != nil {
		return err
	}
	if reply != n.reply(reply.Reason) {
		return fmt.Errorf("%w: answer for another claim", ErrProtocol)
	}
	return cs.ClaimNoticeAnswered(ctx, s.Contact, reply)
}

// receiveClaimNotices takes the peer's claim states, answering each, until
// its End.
func (s Session) receiveClaimNotices(ctx context.Context, r *Result) error {
	cs, _ := s.claims()
	for count := 0; ; count++ {
		frame, err := s.Frames.Recv()
		if err != nil {
			return err
		}
		if kind, _ := Kind(frame); kind == FrameEnd {
			return DecodeEnd(frame)
		}
		if count >= SessionMessages {
			return fmt.Errorf("%w: more claim notices than a session carries", ErrProtocol)
		}
		if err := s.answerClaimNotice(ctx, cs, frame, r); err != nil {
			return err
		}
	}
}

func (s Session) answerClaimNotice(ctx context.Context, cs ClaimStore, frame []byte, r *Result) error {
	n, err := DecodeClaimNotice(frame)
	if err != nil {
		return err
	}
	answer, err := cs.AcceptClaimNotice(ctx, s.Contact, n, s.Now())
	if err != nil {
		return err
	}
	if answer.Stored {
		r.ClaimNoticesStored++
	} else {
		r.ClaimNoticesDropped++
	}
	return s.send(EncodeClaimReply(n.reply(answer.Reason)))
}

// claimSessionStore marks the claim half's store errors too.
type claimSessionStore struct {
	sealSessionStore
	cs ClaimStore
}

func (s claimSessionStore) PendingClaimNotices(ctx context.Context, contact string, max int) ([]ClaimNotice, error) {
	out, err := s.cs.PendingClaimNotices(ctx, contact, max)
	return out, storeErr(err)
}

func (s claimSessionStore) ClaimNoticeAnswered(ctx context.Context, contact string, r ClaimReply) error {
	return storeErr(s.cs.ClaimNoticeAnswered(ctx, contact, r))
}

func (s claimSessionStore) AcceptClaimNotice(ctx context.Context, contact string, n ClaimNotice, at time.Time) (ClaimAnswer, error) {
	a, err := s.cs.AcceptClaimNotice(ctx, contact, n, at)
	return a, storeErr(err)
}
