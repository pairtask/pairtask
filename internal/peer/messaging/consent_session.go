package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ConsentStore is what a ConsentPeerVersion session needs besides Store:
// the outcomes this side owes requesters whose requests it held, and the
// holds and outcomes of requests it sent (#293). A session uses it only
// with an OutcomeStore; without both it speaks at most FindingsVersion.
type ConsentStore interface {
	Store
	// PendingConsentOutcomes is up to max outcomes still to send to contact.
	PendingConsentOutcomes(ctx context.Context, contact string, max int) ([]ConsentOutcome, error)
	// ConsentOutcomeAnswered records the contact's answer to one it was sent.
	ConsentOutcomeAnswered(ctx context.Context, contact string, a ConsentOutcomeStored) error
	// HeldBy records the contact's Held for a message this side sent: stored
	// there, its outcome to come. One never sent is ErrProtocol.
	HeldBy(ctx context.Context, contact string, h Held, at time.Time) error
	// AcceptConsentOutcome takes the one outcome of a held request; the same
	// again changes nothing, another is ErrConsentTerminalConflict.
	AcceptConsentOutcome(ctx context.Context, contact string, o ConsentOutcome, at time.Time) error
	// ConsentOutcomeRefused records the contact's refusal of an outcome it
	// was sent: only the matching one settles, as refused, never replayed.
	ConsentOutcomeRefused(ctx context.Context, contact string, r ConsentOutcomeRefused) error
}

// consentCapable: a store that speaks every phase up to ConsentPeerVersion.
func consentCapable(st Store) bool {
	_, consent := st.(ConsentStore)
	_, outcomes := st.(OutcomeStore)
	return consent && outcomes
}

// consent is the store's consent half, when the agreed version uses it.
func (s Session) consent() (ConsentStore, bool) {
	if s.Version < ConsentPeerVersion || !consentCapable(s.Store) {
		return nil, false
	}
	return s.Store.(ConsentStore), true
}

// recordHeld takes the peer's Held for message id: only at a version that
// carries it, and only for the message just sent.
func (s Session) recordHeld(ctx context.Context, id string, frame []byte) error {
	cs, ok := s.consent()
	if !ok {
		return fmt.Errorf("%w: held before version %d", ErrProtocol, ConsentPeerVersion)
	}
	h, err := DecodeHeld(frame)
	if err != nil {
		return err
	}
	if h.ID != id {
		return fmt.Errorf("%w: answer for another message", ErrProtocol)
	}
	return cs.HeldBy(ctx, s.Contact, h, s.Now())
}

// answerHeld tells the peer its message is held here for the operator.
func (s Session) answerHeld(id string, a Answer) error {
	if _, ok := s.consent(); !ok {
		return fmt.Errorf("messaging: store held a message at version %d", s.Version)
	}
	return s.send(EncodeHeld(Held{ID: id, HeldID: a.HeldID}))
}

// sendConsentOutcomes sends this side's pending consent outcomes, each
// settled on its answer before the next.
func (s Session) sendConsentOutcomes(ctx context.Context, r *Result) error {
	cs, _ := s.consent()
	pending, err := cs.PendingConsentOutcomes(ctx, s.Contact, SessionMessages)
	if err != nil {
		return err
	}
	for _, o := range pending {
		if err := s.sendConsentOutcome(ctx, consentSent{cs, o, r}); err != nil {
			return err
		}
	}
	return s.send(EncodeEnd())
}

// consentSent is one outcome sent, the store that settles it, and the
// session's result that counts it.
type consentSent struct {
	cs ConsentStore
	o  ConsentOutcome
	r  *Result
}

// sendConsentOutcome sends one outcome and settles it by the answer:
// stored, or refused by the requester. Each is counted once settled.
func (s Session) sendConsentOutcome(ctx context.Context, c consentSent) error {
	if err := s.send(EncodeConsentOutcome(c.o)); err != nil {
		return err
	}
	frame, err := s.Frames.Recv()
	if err != nil {
		return err
	}
	if kind, _ := Kind(frame); kind == FrameConsentOutcomeRefused {
		return s.recordConsentRefusal(ctx, c, frame)
	}
	ack, err := DecodeConsentOutcomeStored(frame)
	if err != nil {
		return err
	}
	if ack.ID != c.o.ID {
		return fmt.Errorf("%w: answer for another consent outcome", ErrProtocol)
	}
	if err := c.cs.ConsentOutcomeAnswered(ctx, s.Contact, ack); err != nil {
		return err
	}
	c.r.ConsentOutcomesSent++
	return nil
}

// recordConsentRefusal settles an outcome the requester refused. A
// refusal naming another request settles nothing and the session goes on:
// the outcome stays pending for the next.
func (s Session) recordConsentRefusal(ctx context.Context, c consentSent, frame []byte) error {
	refusal, err := DecodeConsentOutcomeRefused(frame)
	if err != nil {
		return err
	}
	if refusal.ID != c.o.ID {
		return nil // a refusal of another request settles nothing; this one stays pending
	}
	if err := c.cs.ConsentOutcomeRefused(ctx, s.Contact, refusal); err != nil {
		return err
	}
	c.r.ConsentOutcomesRefused++
	return nil
}

// receiveConsentOutcomes takes the peer's consent outcomes, answering
// each once stored, until its End.
func (s Session) receiveConsentOutcomes(ctx context.Context, r *Result) error {
	cs, _ := s.consent()
	for count := 0; ; count++ {
		frame, err := s.Frames.Recv()
		if err != nil {
			return err
		}
		if kind, _ := Kind(frame); kind == FrameEnd {
			return DecodeEnd(frame)
		}
		if count >= SessionMessages {
			return fmt.Errorf("%w: more consent outcomes than a session carries", ErrProtocol)
		}
		if err := s.answerConsentOutcome(ctx, cs, frame, r); err != nil {
			return err
		}
	}
}

// answerConsentOutcome stores one outcome, counts it, then answers: a lost
// answer still moved, and the replay is answered again. One contradicting
// the outcome already held is refused and reported, and the session goes
// on.
func (s Session) answerConsentOutcome(ctx context.Context, cs ConsentStore, frame []byte, r *Result) error {
	o, err := DecodeConsentOutcome(frame)
	if err != nil {
		return err
	}
	err = cs.AcceptConsentOutcome(ctx, s.Contact, o, s.Now())
	if errors.Is(err, ErrConsentTerminalConflict) {
		return s.refuseConsentOutcome(o.ID, err, r)
	}
	if err != nil {
		return err
	}
	r.ConsentOutcomesStored++
	return s.send(EncodeConsentOutcomeStored(ConsentOutcomeStored{ID: o.ID}))
}

// refuseConsentOutcome answers a contradicting outcome as a terminal
// conflict, never stored.
func (s Session) refuseConsentOutcome(id string, _ error, r *Result) error {
	r.ConsentConflicts = append(r.ConsentConflicts, id)
	return s.send(EncodeConsentOutcomeRefused(ConsentOutcomeRefused{ID: id, Reason: ConsentRefusedTerminalConflict}))
}

// consentSessionStore marks the consent half's store errors too; the
// peer's protocol errors stay unmarked.
type consentSessionStore struct {
	outcomeSessionStore
	cs ConsentStore
}

func (s consentSessionStore) PendingConsentOutcomes(ctx context.Context, contact string, max int) ([]ConsentOutcome, error) {
	out, err := s.cs.PendingConsentOutcomes(ctx, contact, max)
	return out, storeErr(err)
}

func (s consentSessionStore) ConsentOutcomeAnswered(ctx context.Context, contact string, a ConsentOutcomeStored) error {
	return storeErr(s.cs.ConsentOutcomeAnswered(ctx, contact, a))
}

func (s consentSessionStore) HeldBy(ctx context.Context, contact string, h Held, at time.Time) error {
	return peerErr(s.cs.HeldBy(ctx, contact, h, at), ErrProtocol)
}

func (s consentSessionStore) AcceptConsentOutcome(ctx context.Context, contact string, o ConsentOutcome, at time.Time) error {
	return peerErr(s.cs.AcceptConsentOutcome(ctx, contact, o, at), ErrProtocol)
}

func (s consentSessionStore) ConsentOutcomeRefused(ctx context.Context, contact string, r ConsentOutcomeRefused) error {
	return storeErr(s.cs.ConsentOutcomeRefused(ctx, contact, r))
}
