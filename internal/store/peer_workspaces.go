package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

// An agent writes to a paired workspace it names (#222): by the contact's
// identity key, or a name when exactly one confirmed contact has it: what
// this side's operator calls it, or what it calls itself (#274).
// Only an agent shared with that contact may; the message is kept in
// history and queued in one transaction, as a reply is.

var (
	// ErrNoWorkspace is a workspace no confirmed contact is.
	ErrNoWorkspace = errors.New("no confirmed paired workspace has that key or name")
	// ErrAmbiguousWorkspace is a name more than one confirmed contact has.
	ErrAmbiguousWorkspace = errors.New("more than one paired workspace has that name; use its key")
)

// ResolveWorkspace is the confirmed contact a workspace names: its full
// identity key, which wins over any name, or the one contact with that name,
// counted once though both its names match. Empty never matches, and
// neither does a prefix.
func (s *Store) ResolveWorkspace(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", ErrNoWorkspace
	}
	contacts, err := s.confirmedContacts(ctx)
	if err != nil {
		return "", err
	}
	var named []string
	for _, c := range contacts {
		if c.IdentityKey == ref {
			return ref, nil
		}
		if c.LocalName == ref || c.Name == ref {
			named = append(named, c.IdentityKey)
		}
	}
	return oneNamed(named)
}

func oneNamed(named []string) (string, error) {
	switch len(named) {
	case 0:
		return "", ErrNoWorkspace
	case 1:
		return named[0], nil
	}
	return "", ErrAmbiguousWorkspace
}

// AgentWorkspaces are the confirmed contacts agent is shared with, the only
// workspaces it may write to.
func (s *Store) AgentWorkspaces(ctx context.Context, agent string) ([]peer.Contact, error) {
	contacts, err := s.confirmedContacts(ctx)
	if err != nil {
		return nil, err
	}
	var out []peer.Contact
	for _, c := range contacts {
		shared, err := allGranted(ctx, s.db, c.IdentityKey, []string{agent})
		if err != nil {
			return nil, err
		}
		if shared {
			out = append(out, c)
		}
	}
	return out, nil
}

// confirmedContacts are the contacts both sides confirmed.
func (s *Store) confirmedContacts(ctx context.Context) ([]peer.Contact, error) {
	contacts, err := s.Contacts(ctx)
	if err != nil {
		return nil, err
	}
	var out []peer.Contact
	for _, c := range contacts {
		ok, err := confirmedContact(ctx, s.db, contactKey(c.IdentityKey))
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// WorkspaceSend is a local agent's message to a paired workspace: to one of
// its agents, or to its operator.
type WorkspaceSend struct {
	Msg           protocol.Message // as routed here: its ID, seq, sender and recipient
	SenderSession string
	Contact       string // the workspace's identity key
	Text          string
	Review        *messaging.ReviewAsk // a review request for Msg.To there (#235)
	Now           time.Time
}

// QueueWorkspaceSend keeps the message in history and queues it to the
// contact in one transaction. The sender must be shared with the contact
// now, or it is messaging.ErrNotShared; a retry of the same message to the
// same contact is answered as first queued, and its ID used for anything
// else is a conflict.
func (s *Store) QueueWorkspaceSend(ctx context.Context, w WorkspaceSend) (RemoteReplyResult, error) {
	m := messaging.Message{ID: w.Msg.MessageID, Text: w.Text, From: w.Msg.From, Review: w.Review}
	if w.Msg.To != protocol.OperatorID {
		m.To = []string{w.Msg.To}
	}
	if !validOutgoing(m) {
		return RemoteReplyResult{}, fmt.Errorf("%w: ID, text or routing", messaging.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RemoteReplyResult{}, err
	}
	defer tx.Rollback()
	o := outgoing{key: messageKey{w.Contact, messaging.DirOut, m.ID}, m: m, now: w.Now, localID: m.ID}
	return queueFromAgent(ctx, tx, agentMessage{local: w.Msg, session: w.SenderSession, out: o})
}
