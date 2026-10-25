package hub

import (
	"context"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// claimFrames are the agent's claim requests (#141, v8).
var claimFrames = map[string]bool{
	protocol.TypeClaimSet: true, protocol.TypeClaimRelease: true,
	protocol.TypeClaimDone: true, protocol.TypeClaimGet: true,
}

// claimErrors maps store outcomes to the answer an agent sees. Every target
// failure reads the same, so a claim never reveals whether a message exists.
var claimErrors = []struct {
	err  error
	code string
}{
	{store.ErrClaimTarget, protocol.ErrInvalidClaim},
	{store.ErrClaimNotHeld, protocol.ErrInvalidClaim},
	{store.ErrClaimBothRoles, protocol.ErrInvalidClaim},
	{store.ErrClaimsFull, protocol.ErrInvalidClaim},
	{store.ErrClaimConflict, protocol.ErrClaimConflict},
}

// handleClaim serves one claim frame. A change is confirmed, and the operator
// told, only after it committed; a conflict changes nothing and names the
// holder.
func (h *Hub) handleClaim(ctx context.Context, clients map[string]*Client, in Inbound) {
	h.enqueue(clients, in.From, h.claim(ctx, in))
}

func (h *Hub) claim(ctx context.Context, in Inbound) protocol.Message {
	msg := in.Msg
	if msg.RequestID == "" || len(msg.RequestID) > 128 {
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, "request_id is required, at most 128 bytes")
	}
	if in.From.ProtocolVersion < protocol.ClaimVersion {
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, "claims require protocol_version 8")
	}
	if err := validClaimFrame(msg); err != nil {
		return statusError(msg.RequestID, protocol.ErrInvalidClaim, err.Error())
	}
	now := time.Now().UTC()
	if msg.Type != protocol.TypeClaimGet && !h.statuses.takeClaim(in.From.ID, now) {
		return statusError(msg.RequestID, protocol.ErrClaimRateLimited, "too many claim changes; retry shortly")
	}
	req := store.ClaimRequest{MessageID: msg.OperatorMessageID, Task: msg.Task, Topic: msg.Topic, Role: msg.Role, Agent: in.From.ID, At: now}
	set, err := h.runClaim(ctx, msg.Type, req)
	if err != nil {
		return h.claimFailure(msg.RequestID, set, err)
	}
	if msg.Type != protocol.TypeClaimGet {
		h.emit(protocol.SessionEvent{Kind: protocol.EventClaimsChanged, OperatorMessageID: msg.OperatorMessageID})
	}
	return protocol.Message{Type: protocol.TypeClaimResult, RequestID: msg.RequestID, OperatorMessageID: msg.OperatorMessageID,
		ClaimPage: page(set)}
}

func (h *Hub) runClaim(ctx context.Context, kind string, req store.ClaimRequest) (protocol.ClaimSet, error) {
	switch kind {
	case protocol.TypeClaimSet:
		return h.store.SetClaim(ctx, req)
	case protocol.TypeClaimRelease:
		return h.store.ReleaseClaim(ctx, req)
	case protocol.TypeClaimDone:
		return h.store.FinishClaim(ctx, req)
	}
	return h.store.Claims(ctx, req.MessageID, req.Agent)
}

// claimFailure answers a refused claim. A conflict carries the message's
// claims, which name the holder; a target failure carries nothing.
func (h *Hub) claimFailure(request string, set protocol.ClaimSet, err error) protocol.Message {
	for _, m := range claimErrors {
		if errors.Is(err, m.err) {
			frame := statusError(request, m.code, m.err.Error())
			if m.code == protocol.ErrClaimConflict {
				frame.OperatorMessageID, frame.ClaimPage = set.OperatorMessageID, page(set)
			}
			return frame
		}
	}
	h.log.Error("claim not stored", "request_id", request, "error", err)
	return statusError(request, protocol.ErrInternal, "claim not stored")
}

// page is a set's claims as a frame field; claims is never null.
func page(set protocol.ClaimSet) *protocol.ClaimPage {
	p := set.ClaimPage
	if p.Claims == nil {
		p.Claims = []protocol.Claim{}
	}
	return &p
}

// validClaimFrame checks the fields each claim frame needs.
func validClaimFrame(msg protocol.Message) error {
	if err := protocol.ValidateClaimMessage(msg.OperatorMessageID); err != nil {
		return err
	}
	if msg.Type == protocol.TypeClaimGet {
		return nil
	}
	if err := protocol.ValidateTask(msg.Task); err != nil {
		return err
	}
	switch msg.Type {
	case protocol.TypeClaimSet:
		if err := protocol.ValidateRole(msg.Role); err != nil {
			return err
		}
		return protocol.ValidateTopic(msg.Topic)
	case protocol.TypeClaimRelease:
		return protocol.ValidateRole(msg.Role)
	}
	return nil
}

// snapshotClaims is the claims of the newest operator messages that have any,
// with the total so an omission stays visible.
func (h *Hub) snapshotClaims(ctx context.Context) ([]protocol.ClaimSet, int, error) {
	return h.store.RecentClaims(ctx, protocol.SnapshotClaimed)
}
