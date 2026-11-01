package hub

import (
	"context"
	"errors"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Review requests with a paired workspace (#235), for the operator: what
// the contact may ask of agents here, a request for one of its agents, and
// the decision on one of its requests that waits for it. Each is audited
// before it changes anything.

// contactReviewGrant sets the contact's review grant for the named agents,
// then imports what it settles and answers with the newest page.
func (h *Hub) contactReviewGrant(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	g := msg.ContactReviewGrant
	change := store.ReviewGrantChange{Agents: msg.ContactAgents, Mode: g.Mode, Publish: g.Publish, Formal: g.Formal, Now: h.contactNow()}
	if _, err := h.store.PeerMessages().SetReviewGrant(ctx, msg.ContactKey, change); err != nil {
		return protocol.Message{}, err
	}
	return h.contactChanged(ctx, req, auditID)
}

// contactReviewAsk asks the contact's one agent in contact_to for a review,
// queued like a send to its agents.
func (h *Hub) contactReviewAsk(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	ask := msg.ReviewAsk
	text := msg.ContactText
	if text == "" {
		text = "Review request: " + ask.Repository + "#" + ask.Change
	}
	row, err := h.store.OperatorContactSend(ctx, store.ContactSend{OperationID: msg.MessageID, Contact: msg.ContactKey, Text: text,
		To: msg.ContactTo, Review: wireAsk(ask), AuditID: auditID, Now: h.contactNow()})
	if err != nil {
		return protocol.Message{}, err
	}
	h.SyncMessages(msg.ContactKey)
	h.MessagesChanged(msg.ContactKey)
	view := contactMessageView(row)
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp, ContactMessage: &view}, nil
}

// contactReviewDecide approves or declines the contact's request that
// waits for this side's operator; an approved one is dispatched next.
func (h *Hub) contactReviewDecide(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	_, err := h.store.DecideRemoteReview(ctx, store.RemoteDecision{Contact: msg.ContactKey, RemoteID: msg.ContactRead[0],
		Approve: *msg.ContactApprove, Now: h.contactNow()})
	if err != nil {
		return protocol.Message{}, err
	}
	return h.contactChanged(ctx, req, auditID)
}

// contactChanged finishes an accepted change's audit, imports what it
// settles, and answers with the newest page.
func (h *Hub) contactChanged(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	if err := h.store.FinishOperatorAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditAccepted}); err != nil {
		return protocol.Message{}, err
	}
	h.importsDue()
	h.SyncMessages(msg.ContactKey) // outcomes it queued leave at once
	h.MessagesChanged(msg.ContactKey)
	return h.contactList(ctx, protocol.Message{MessageID: msg.MessageID, RequestID: msg.RequestID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp}), nil
}

// reviewRefusals are the review ops' refusals the operator can act on.
var reviewRefusals = []struct {
	err    error
	detail string
}{
	{store.ErrNotAwaiting, "that request isn't waiting for a decision"},
	{store.ErrNoImport, "no review request from this contact has that ID"},
	{store.ErrReviewGrant, "a review grant is ask, auto or off for named agents; formal needs publish"},
}

func reviewRefusal(err error) (*routingError, bool) {
	for _, r := range reviewRefusals {
		if errors.Is(err, r.err) {
			return &routingError{protocol.ErrContactRefused, r.detail}, true
		}
	}
	return nil, false
}

// reviewView adds a review request's ask to its view, and for one the
// contact sent, where it is.
func (h *Hub) reviewView(ctx context.Context, view *protocol.ContactMessageView, r messaging.Row) error {
	if r.Review == nil {
		return nil
	}
	a := r.Review
	view.ReviewAsk = &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: a.Forge, Repository: a.Repository, Change: a.Change},
		Scope: a.Scope, Base: a.Base, Head: a.Head, Publish: a.Publish, Formal: a.Formal}
	if r.Direction != messaging.DirIn {
		a, found, err := h.store.PeerMessages().AskOutFor(ctx, r.Contact, r.ID)
		if found {
			outcome := a.View()
			view.ReviewOutcome = &outcome
		}
		return err
	}
	if r.State == messaging.StateRefused {
		// Refused as not shared and recorded here: there is no ask behind it.
		view.ReviewState, view.ReviewReason = messaging.StateRefused, r.Refusal.Reason
		return nil
	}
	status, _, err := h.store.PeerMessages().AskStatus(ctx, r.Contact, r.ID)
	view.ReviewState, view.ReviewID, view.ReviewReason = status.State, status.ReviewID, status.Reason
	return err
}

// grantViews are the contact's review grants as the operator sees them.
func (h *Hub) grantViews(ctx context.Context, contact string) ([]protocol.ReviewGrantView, error) {
	grants, err := h.store.PeerMessages().ReviewGrants(ctx, contact)
	var out []protocol.ReviewGrantView
	for _, g := range grants {
		out = append(out, protocol.ReviewGrantView{Agent: g.Agent, Mode: g.Mode, Publish: g.Publish, Formal: g.Formal})
	}
	return out, err
}
