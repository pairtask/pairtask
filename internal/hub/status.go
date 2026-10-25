package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type statusRequest struct {
	ctx   context.Context
	reply chan protocol.StatusSnapshot
}

// Status serializes observation with routing, without registering an agent or
// allocating a message sequence. The buffered reply cannot block the hub if
// the inspecting client disappears while the snapshot is being read.
func (h *Hub) Status(ctx context.Context) (protocol.StatusSnapshot, error) {
	req := statusRequest{ctx: ctx, reply: make(chan protocol.StatusSnapshot, 1)}
	select {
	case <-ctx.Done():
		return protocol.StatusSnapshot{}, ctx.Err()
	case <-h.done:
		return protocol.StatusSnapshot{}, errors.New("broker stopped")
	case h.status <- req:
	}
	select {
	case <-ctx.Done():
		return protocol.StatusSnapshot{}, ctx.Err()
	case <-h.done:
		return protocol.StatusSnapshot{}, errors.New("broker stopped")
	case out := <-req.reply:
		return out, nil
	}
}

func (h *Hub) snapshot(ctx context.Context, clients map[string]*Client) protocol.StatusSnapshot {
	now := time.Now().UTC()
	count := len(clients)
	out := protocol.UnavailableStatus()
	out.Reachable, out.Health = true, "ready"
	out.SnapshotAt, out.BrokerStartedAt = &now, &h.startedAt
	out.ProtocolVersion, out.ConnectedSessions = protocol.Version, &count
	out.Sessions, out.SessionsTruncated = liveSessions(clients)
	// Status storage reads must not hold up routing indefinitely.
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	records, err := h.store.ReadStatus(ctx)
	if err != nil {
		h.log.Error("status storage query failed", "error", err)
		out.Health, out.Error = "degraded", "session history and pending counts unavailable"
		return out
	}
	out.HistoryAvailable = true
	out.DurablePending = &records.Pending
	out.OperatorPending = &records.OperatorPending
	appendHistory(&out, clients, records.Sessions)
	h.snapshotAgentStatus(ctx, clients, &out)
	h.snapshotClaimSets(ctx, &out)
	return fitStatus(out)
}

// fitStatus drops the oldest claimed messages, then the last declarations,
// until the status frame fits; the totals keep the omission visible.
func fitStatus(out protocol.StatusSnapshot) protocol.StatusSnapshot {
	for {
		data, err := json.Marshal(protocol.Message{Type: protocol.TypeStatus, Status: &out})
		if err != nil || len(data)+statusReserve <= protocol.MaxFrameBytes {
			return out
		}
		switch {
		case len(out.Claims) > 0:
			out.Claims, out.ClaimsTruncated = out.Claims[:len(out.Claims)-1], true
		case len(out.AgentStatus) > 0:
			out.AgentStatus, out.AgentStatusTruncated = out.AgentStatus[:len(out.AgentStatus)-1], true
		default:
			return out
		}
	}
}

// statusReserve leaves room for the frame's newline.
const statusReserve = 16

// snapshotClaimSets adds the claims, as the operator snapshot has them. A
// failed read leaves them unavailable rather than empty.
func (h *Hub) snapshotClaimSets(ctx context.Context, out *protocol.StatusSnapshot) {
	sets, total, err := h.snapshotClaims(ctx)
	if err != nil {
		h.log.Error("claims query failed", "error", err)
		out.Health, out.Error = "degraded", joinError(out.Error, "claims unavailable")
		return
	}
	if sets != nil {
		out.Claims = sets
	}
	out.ClaimsTotal, out.ClaimsTruncated, out.ClaimsAvailable = total, total > len(sets), true
}

func joinError(prev, next string) string {
	if prev == "" {
		return next
	}
	return prev + "; " + next
}

// snapshotAgentStatus adds the agents' declarations, as the operator snapshot
// has them. A failed read leaves them unavailable rather than empty.
func (h *Hub) snapshotAgentStatus(ctx context.Context, clients map[string]*Client, out *protocol.StatusSnapshot) {
	records, total, err := h.snapshotStatuses(ctx, clients)
	if err != nil {
		h.log.Error("agent status query failed", "error", err)
		out.Health, out.Error = "degraded", joinError(out.Error, "agent status unavailable")
		return
	}
	if records != nil {
		out.AgentStatus = records
	}
	out.AgentStatusTotal, out.AgentStatusTruncated, out.AgentStatusAvailable = total, total > len(records), true
}

func liveSessions(clients map[string]*Client) ([]protocol.SessionStatus, bool) {
	ids := make([]string, 0, len(clients))
	for id := range clients {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	truncated := len(ids) > protocol.StatusLimit
	ids = ids[:min(len(ids), protocol.StatusLimit)]
	out := make([]protocol.SessionStatus, 0, len(ids))
	for _, id := range ids {
		c := clients[id]
		seen := c.lastSeenAt
		out = append(out, protocol.SessionStatus{AgentID: c.ID, SessionID: c.SessionID,
			State: "transport_connected", ConnectedAt: c.connectedAt, LastSeenAt: &seen})
	}
	return out, truncated
}

func appendHistory(out *protocol.StatusSnapshot, clients map[string]*Client, history []protocol.SessionStatus) {
	if len(history) > protocol.StatusLimit {
		out.SessionsTruncated = true
	}
	for _, session := range history {
		if c := clients[session.AgentID]; c != nil && c.SessionID == session.SessionID {
			continue
		}
		if len(out.Sessions) == protocol.StatusLimit {
			out.SessionsTruncated = true
			return
		}
		out.Sessions = append(out.Sessions, session)
	}
}
