package messaging

import (
	"context"
	"fmt"
	"time"
)

// OutcomeStore is what a version 5 session needs besides RoutingStore:
// the outcomes this side owes requesters, and the outcomes of requests it
// sent. A store without it speaks at most version 4.
type OutcomeStore interface {
	RoutingStore
	// PendingOutcomes is up to max outcomes still to send to contact, the
	// latest of each request.
	PendingOutcomes(ctx context.Context, contact string, max int) ([]ReviewOutcome, error)
	// OutcomeAnswered records the contact's answer to one it was sent: a
	// queued outcome no newer than the answer settles; a refusal is kept.
	OutcomeAnswered(ctx context.Context, contact string, r OutcomeReply) error
	// AcceptOutcome takes the contact's outcome of a request this side sent.
	AcceptOutcome(ctx context.Context, contact string, o ReviewOutcome, at time.Time) (OutcomeAnswer, error)
}

// outcomes is the store's outcome half, when the agreed version uses it.
func (s Session) outcomes() (OutcomeStore, bool) {
	if s.Version < OutcomeVersion {
		return nil, false
	}
	o, ok := s.Store.(OutcomeStore)
	return o, ok
}

// sendOutcomes sends this side's pending outcomes, each answered before
// the next: a transient refusal leaves it to send again, anything else
// settles it as the store decides.
func (s Session) sendOutcomes(ctx context.Context, r *Result) error {
	os, _ := s.outcomes()
	pending, err := os.PendingOutcomes(ctx, s.Contact, SessionMessages)
	if err != nil {
		return err
	}
	for _, o := range pending {
		settled, err := s.sendOutcome(ctx, os, o)
		if err != nil {
			return err
		}
		if settled {
			r.OutcomesSent++
		}
	}
	return s.send(EncodeEnd())
}

// sendOutcome sends one outcome and records the answer; settled reports
// that the answer was recorded.
func (s Session) sendOutcome(ctx context.Context, os OutcomeStore, o ReviewOutcome) (bool, error) {
	if err := s.send(EncodeOutcome(o)); err != nil {
		return false, err
	}
	frame, err := s.Frames.Recv()
	if err != nil {
		return false, err
	}
	reply, err := DecodeOutcomeReply(frame)
	if err != nil {
		return false, err
	}
	if reply.AskID != o.AskID || reply.Seq != o.Seq {
		return false, fmt.Errorf("%w: answer for another outcome", ErrProtocol)
	}
	if reasonClass[reply.Reason] == Transient {
		return false, nil // offered again next session
	}
	return true, os.OutcomeAnswered(ctx, s.Contact, reply)
}

// receiveOutcomes takes the peer's outcomes, answering each, until its End.
func (s Session) receiveOutcomes(ctx context.Context, r *Result) error {
	os, _ := s.outcomes()
	for count := 0; ; count++ {
		frame, err := s.Frames.Recv()
		if err != nil {
			return err
		}
		if kind, _ := Kind(frame); kind == FrameEnd {
			return DecodeEnd(frame)
		}
		if count >= SessionMessages {
			return fmt.Errorf("%w: more outcomes than a session carries", ErrProtocol)
		}
		stored, err := s.answerOutcome(ctx, os, frame)
		if err != nil {
			return err
		}
		if stored {
			r.OutcomesStored++
		}
	}
}

// answerOutcome takes one outcome and answers it; stored reports that the
// store took it.
func (s Session) answerOutcome(ctx context.Context, os OutcomeStore, frame []byte) (bool, error) {
	o, err := DecodeOutcome(frame)
	if err != nil {
		return false, err
	}
	answer, err := os.AcceptOutcome(ctx, s.Contact, o, s.Now())
	if err != nil {
		return false, err
	}
	reply := OutcomeReply{AskID: o.AskID, Seq: o.Seq}
	if !answer.Stored {
		reply.Reason = answer.Reason
	}
	return answer.Stored, s.send(EncodeOutcomeReply(reply))
}

// outcomeSessionStore marks the outcome half's store errors too.
type outcomeSessionStore struct {
	routingSessionStore
	os OutcomeStore
}

func (s outcomeSessionStore) PendingOutcomes(ctx context.Context, contact string, max int) ([]ReviewOutcome, error) {
	out, err := s.os.PendingOutcomes(ctx, contact, max)
	return out, storeErr(err)
}

func (s outcomeSessionStore) OutcomeAnswered(ctx context.Context, contact string, r OutcomeReply) error {
	return storeErr(s.os.OutcomeAnswered(ctx, contact, r))
}

func (s outcomeSessionStore) AcceptOutcome(ctx context.Context, contact string, o ReviewOutcome, at time.Time) (OutcomeAnswer, error) {
	a, err := s.os.AcceptOutcome(ctx, contact, o, at)
	return a, storeErr(err)
}
