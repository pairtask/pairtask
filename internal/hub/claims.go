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
	now := time.Now().UTC()
	if refusal := h.claimPreflight(in, now); refusal != nil {
		return *refusal
	}
	req := store.ClaimRequest{MessageID: msg.OperatorMessageID, Task: msg.Task, Topic: msg.Topic, Role: msg.Role, Agent: in.From.ID, At: now}
	set, err := h.runClaim(ctx, msg.Type, req)
	if err != nil {
		failure := h.claimFailure(msg.RequestID, set, err)
		failure.ClaimPage = forVersion(failure.ClaimPage, in.From.ProtocolVersion)
		return failure
	}
	if msg.Type != protocol.TypeClaimGet {
		h.emit(protocol.SessionEvent{Kind: protocol.EventClaimsChanged, OperatorMessageID: msg.OperatorMessageID})
	}
	return protocol.Message{Type: protocol.TypeClaimResult, RequestID: msg.RequestID, OperatorMessageID: msg.OperatorMessageID,
		ClaimPage: forVersion(page(set), in.From.ProtocolVersion)}
}

// claimPreflight refuses a frame before the store sees it: a missing
// request_id, an old protocol, bad fields, or a spent change budget.
func (h *Hub) claimPreflight(in Inbound, now time.Time) *protocol.Message {
	msg := in.Msg
	refuse := func(code, detail string) *protocol.Message {
		frame := statusError(msg.RequestID, code, detail)
		return &frame
	}
	switch {
	case msg.RequestID == "" || len(msg.RequestID) > 128:
		return refuse(protocol.ErrMalformedFrame, "request_id is required, at most 128 bytes")
	case in.From.ProtocolVersion < protocol.ClaimVersion:
		return refuse(protocol.ErrMalformedFrame, "claims require protocol_version 8")
	}
	if err := validClaimFrame(msg); err != nil {
		return refuse(protocol.ErrInvalidClaim, err.Error())
	}
	if msg.Type != protocol.TypeClaimGet && !h.statuses.takeClaim(in.From, now) {
		return refuse(protocol.ErrClaimRateLimited, "too many claim changes; retry shortly")
	}
	return nil
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
	case protocol.TypeClaimDone:
		// The role may be left out (done is the owner's); one given must
		// be a client's role, never an assignment's (#278).
		if msg.Role != "" {
			return protocol.ValidateRole(msg.Role)
		}
	}
	return nil
}

// snapshotClaims is the claims of the newest operator messages that have any,
// with the total so an omission stays visible.
func (h *Hub) snapshotClaims(ctx context.Context) ([]protocol.ClaimSet, int, error) {
	return h.store.RecentClaims(ctx, protocol.SnapshotClaimed)
}

// bucketMap holds one token bucket per agent.
type bucketMap map[string]*tokenBucket

// of is the agent's bucket, created full.
func (m bucketMap) of(agent string, now time.Time) *tokenBucket {
	b := m[agent]
	if b == nil {
		b = &tokenBucket{tokens: statusBurst, at: now}
		m[agent] = b
	}
	return b
}
