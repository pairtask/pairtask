package hub

import (
	"context"
	"maps"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Agent roles (#339, calibration): an agent declares its own role and reads
// how the shadow classifier's picks compared with the replies it claimed.
// The role reaches the model's roster; nothing else changes.

var roleFrames = map[string]bool{protocol.TypeRoleSet: true, protocol.TypeClassifierFeedbackGet: true}

func (h *Hub) handleRole(ctx context.Context, clients map[string]*Client, in Inbound) {
	h.enqueue(clients, in.From, h.roleAnswer(ctx, in))
}

func (h *Hub) roleAnswer(ctx context.Context, in Inbound) protocol.Message {
	msg := in.Msg
	if refusal := rolePreflight(in); refusal != nil {
		return *refusal
	}
	if msg.Type == protocol.TypeClassifierFeedbackGet {
		return h.feedbackAnswer(ctx, in)
	}
	if !h.statuses.takeClaim(in.From, time.Now().UTC()) {
		return statusError(msg.RequestID, protocol.ErrClaimRateLimited, "too many role changes; retry shortly")
	}
	role, err := h.store.SetAgentRole(ctx, in.From.ID, msg.AgentRoleText, time.Now().UTC())
	if err != nil {
		h.log.Error("role not stored", "agent", in.From.ID, "error", err)
		return statusError(msg.RequestID, protocol.ErrInternal, "role not stored")
	}
	return protocol.Message{Type: protocol.TypeRoleResult, RequestID: msg.RequestID, AgentRole: &role}
}

// rolePreflight refuses a frame before the store sees it.
func rolePreflight(in Inbound) *protocol.Message {
	msg := in.Msg
	refuse := func(code, detail string) *protocol.Message {
		frame := statusError(msg.RequestID, code, detail)
		return &frame
	}
	switch {
	case msg.RequestID == "" || len(msg.RequestID) > 128:
		return refuse(protocol.ErrMalformedFrame, "request_id is required, at most 128 bytes")
	case in.From.ProtocolVersion < protocol.RoleVersion:
		return refuse(protocol.ErrForbidden, "roles need protocol version 28")
	}
	if err := protocol.ValidateAgentRole(msg.AgentRoleText); err != nil {
		return refuse(protocol.ErrMalformedFrame, err.Error())
	}
	return nil
}

// feedbackAnswer is the agent's own view of the shadow classifier.
func (h *Hub) feedbackAnswer(ctx context.Context, in Inbound) protocol.Message {
	msg := in.Msg
	feedback, err := h.feedback(ctx, in.From.ID, protocol.FeedbackLimit(msg.FeedbackLimit))
	if err != nil {
		h.log.Error("classifier feedback unavailable", "agent", in.From.ID, "error", err)
		return statusError(msg.RequestID, protocol.ErrInternal, "classifier feedback unavailable")
	}
	return protocol.Message{Type: protocol.TypeClassifierFeedbackResult, RequestID: msg.RequestID, ClassifierFeedback: &feedback}
}

func (h *Hub) feedback(ctx context.Context, agent string, listed int) (protocol.ClassifierFeedback, error) {
	out := protocol.ClassifierFeedback{AgentID: agent, Enabled: h.classify.cfg != nil, Counts: map[string]int{}, Items: []protocol.FeedbackItem{}}
	roles, err := h.rosterRoles(ctx)
	if err != nil {
		return out, err
	}
	out.Role = roles[agent]
	rows, err := h.store.PredictionsFor(ctx, agent, protocol.FeedbackWindow)
	if err != nil {
		return out, err
	}
	for _, p := range rows {
		item, ok, err := h.feedbackItem(ctx, agent, p)
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		out.Counts[item.Verdict]++
		if len(out.Items) < listed {
			out.Items = append(out.Items, item)
		}
	}
	return out, nil
}

// feedbackItem is the prediction as it concerns the agent; ok is false when
// it doesn't: the model didn't pick it and it didn't claim the reply.
func (h *Hub) feedbackItem(ctx context.Context, agent string, p store.Prediction) (protocol.FeedbackItem, bool, error) {
	claimant, err := h.store.ReplyOwner(ctx, p.MessageID)
	if err != nil {
		return protocol.FeedbackItem{}, false, err
	}
	verdict := feedbackVerdict(agent, p, claimant)
	if verdict == "" {
		return protocol.FeedbackItem{}, false, nil
	}
	return protocol.FeedbackItem{MessageID: p.MessageID, At: p.CreatedAt, Outcome: p.Outcome, Predicted: p.AgentID, Confidence: p.Confidence,
		YourProbability: p.Probabilities[agent], Claimant: claimant, Verdict: verdict}, true, nil
}

// feedbackVerdict is the agent's verdict, or "" when the prediction isn't
// about it or the model gave no answer (skipped, failed, timed out).
func feedbackVerdict(agent string, p store.Prediction, claimant string) string {
	if p.Outcome != store.PredictionPredicted && p.Outcome != store.PredictionAbstain {
		return ""
	}
	picked := p.Outcome == store.PredictionPredicted && p.AgentID == agent
	switch {
	case picked && claimant == agent:
		return protocol.VerdictAgreed
	case claimant == agent:
		return protocol.VerdictMissed
	case picked && claimant != "":
		return protocol.VerdictWrongPick
	case picked:
		return protocol.VerdictUnclaimedPick
	}
	return ""
}

// rosterRoles are the roles the model sees: the standalone file's, overlaid
// by the ones agents declared.
func (h *Hub) rosterRoles(ctx context.Context) (map[string]string, error) {
	roles := map[string]string{}
	if c := h.classify.cfg; c != nil {
		maps.Copy(roles, c.Roles)
	}
	declared, err := h.store.AgentRoles(ctx)
	if err != nil {
		return roles, err
	}
	maps.Copy(roles, declared)
	return roles, nil
}
