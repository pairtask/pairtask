package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Version 3's two extra phase pairs, after the read receipts: each side's
// agent acknowledgments, answered one by one like reads, then each side's
// directory, the agents it shares, answered with End.

// sendAgentReads sends this side's pending agent acknowledgments, confirming
// each on its answer.
func (s Session) sendAgentReads(ctx context.Context, r *Result) error {
	rs, _ := s.routing()
	reads, err := rs.PendingAgentReads(ctx, s.Contact, SessionMessages)
	if err != nil {
		return err
	}
	for _, read := range reads {
		if err := s.sendAgentRead(ctx, rs, read); err != nil {
			return err
		}
		r.ReadsSent++
	}
	return s.send(EncodeEnd())
}

func (s Session) sendAgentRead(ctx context.Context, rs RoutingStore, read AgentRead) error {
	if err := s.send(EncodeAgentRead(read)); err != nil {
		return err
	}
	frame, err := s.Frames.Recv()
	if err != nil {
		return err
	}
	ack, err := DecodeAgentReadStored(frame)
	if err != nil {
		return err
	}
	if ack != read {
		return fmt.Errorf("%w: answer for another agent acknowledgment", ErrProtocol)
	}
	return rs.AgentReadConfirmed(ctx, s.Contact, read)
}

// receiveAgentReads records the peer's agent acknowledgments, answering
// each with both its ID and its agent, until its End. One from an agent the
// message wasn't sent to is a protocol error.
func (s Session) receiveAgentReads(ctx context.Context, r *Result) error {
	rs, _ := s.routing()
	for count := 0; ; count++ {
		frame, err := s.Frames.Recv()
		if err != nil {
			return err
		}
		if kind, _ := Kind(frame); kind == FrameEnd {
			return DecodeEnd(frame)
		}
		if count >= SessionMessages {
			return fmt.Errorf("%w: more agent acknowledgments than a session carries", ErrProtocol)
		}
		read, err := DecodeAgentRead(frame)
		if err != nil {
			return err
		}
		if err := rs.PeerAgentRead(ctx, s.Contact, read, s.Now()); err != nil {
			return err
		}
		r.ReadsStored++
		if err := s.send(EncodeAgentReadStored(read)); err != nil {
			return err
		}
	}
}

// sendDirectory sends the agents this side shares with the contact.
func (s Session) sendDirectory(ctx context.Context, _ *Result) error {
	rs, _ := s.routing()
	agents, err := rs.SharedAgents(ctx, s.Contact)
	if err != nil {
		return err
	}
	if err := s.send(EncodeDirectory(agents)); err != nil {
		return err
	}
	frame, err := s.Frames.Recv()
	if err != nil {
		return err
	}
	return DecodeEnd(frame)
}

// receiveDirectory records what the contact shares, a hint, and answers.
func (s Session) receiveDirectory(ctx context.Context, _ *Result) error {
	rs, _ := s.routing()
	frame, err := s.Frames.Recv()
	if err != nil {
		return err
	}
	agents, err := DecodeDirectory(frame)
	if err != nil {
		return err
	}
	if err := rs.SetPeerDirectory(ctx, s.Contact, agents, s.Now()); err != nil {
		return err
	}
	return s.send(EncodeEnd())
}

// wrapStore marks every store error of a session, keeping the routing half
// when the store has one.
func wrapStore(st Store) Store {
	if rs, ok := st.(RoutingStore); ok {
		return routingSessionStore{sessionStore{rs}, rs}
	}
	return sessionStore{st}
}

type routingSessionStore struct {
	sessionStore
	rs RoutingStore
}

func (s routingSessionStore) PendingAgentReads(ctx context.Context, contact string, max int) ([]AgentRead, error) {
	reads, err := s.rs.PendingAgentReads(ctx, contact, max)
	return reads, storeErr(err)
}

func (s routingSessionStore) AgentReadConfirmed(ctx context.Context, contact string, r AgentRead) error {
	return storeErr(s.rs.AgentReadConfirmed(ctx, contact, r))
}

// PeerAgentRead keeps ErrNotRecipient unmarked: it is the peer's protocol
// error, not this side's store failing.
func (s routingSessionStore) PeerAgentRead(ctx context.Context, contact string, r AgentRead, at time.Time) error {
	return peerErr(s.rs.PeerAgentRead(ctx, contact, r, at), ErrNotRecipient)
}

func (s routingSessionStore) SharedAgents(ctx context.Context, contact string) ([]string, error) {
	agents, err := s.rs.SharedAgents(ctx, contact)
	return agents, storeErr(err)
}

// SetPeerDirectory keeps ErrInvalid unmarked: a directory outside the rules
// is the peer's protocol error.
func (s routingSessionStore) SetPeerDirectory(ctx context.Context, contact string, agents []string, at time.Time) error {
	return peerErr(s.rs.SetPeerDirectory(ctx, contact, agents, at), ErrInvalid)
}

// peerErr is a store error, or a protocol error when it is the peer's: the
// store refused what the peer sent as kind.
func peerErr(err, kind error) error {
	if errors.Is(err, kind) {
		return fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	return storeErr(err)
}
