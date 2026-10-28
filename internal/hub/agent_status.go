package hub

import (
	"context"
	"sort"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Status writes are bounded per agent: a burst of statusBurst, then one token
// every statusRefill. A refused write is answered, never dropped.
const (
	statusBurst  = 10
	statusRefill = 2 * time.Second
	// activityCoalesce spaces activity updates after the first one.
	activityCoalesce = 30 * time.Second
)

// statusState is the hub's per-agent status bookkeeping, owned by Run.
type statusState struct {
	buckets  bucketMap
	claims   bucketMap // claim changes, a separate budget per agent
	reviews  bucketMap // review changes (#201), a separate budget per agent
	activity map[string]sessionActivity
}

type tokenBucket struct {
	tokens float64
	at     time.Time
}

// sessionActivity is the latest activity reported by one session of an agent.
type sessionActivity struct {
	session string
	seen    protocol.Activity
}

func (s *statusState) init() {
	if s.buckets == nil {
		s.buckets = bucketMap{}
		s.claims = bucketMap{}
		s.reviews = bucketMap{}
		s.activity = map[string]sessionActivity{}
	}
}

// forget drops the limiter and activity of a session that disconnected, so
// the bookkeeping is bounded by the connected agents.
func (s *statusState) forget(c *Client) {
	s.init()
	delete(s.buckets, c.ID)
	delete(s.claims, c.ID)
	delete(s.reviews, c.ID)
	if a, ok := s.activity[c.ID]; ok && a.session == c.SessionID {
		delete(s.activity, c.ID)
	}
}

// recentActivity reports activity already applied for this session within
// activityCoalesce.
func (s *statusState) recentActivity(c *Client, now time.Time) bool {
	prev, ok := s.activity[c.ID]
	if !ok || prev.session != c.SessionID {
		return false
	}
	return now.Sub(prev.seen.At) < activityCoalesce
}

// take spends a status token for agent if one is available.
func (s *statusState) take(agent string, now time.Time) bool {
	return s.buckets.of(agent, now).spend(now)
}

// takeClaim spends a claim token for the client's agent; claims have a
// budget of their own.
func (s *statusState) takeClaim(c *Client, now time.Time) bool {
	s.init()
	return s.claims.of(c.ID, now).spend(now)
}

// takeReview spends a review token for the client's agent.
func (s *statusState) takeReview(c *Client, now time.Time) bool {
	s.init()
	return s.reviews.of(c.ID, now).spend(now)
}

// spend takes one token: a burst of statusBurst, then one every statusRefill.
func (b *tokenBucket) spend(now time.Time) bool {
	b.tokens = min(statusBurst, b.tokens+now.Sub(b.at).Seconds()/statusRefill.Seconds())
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// handleStatus serves the three status frames for a registered agent.
func (h *Hub) handleStatus(ctx context.Context, clients map[string]*Client, in Inbound) {
	h.statuses.init()
	if !validRequest(in.Msg) {
		h.enqueue(clients, in.From, statusError(in.Msg.RequestID, protocol.ErrMalformedFrame, "request_id is required, at most 128 bytes"))
		return
	}
	switch in.Msg.Type {
	case protocol.TypeAgentStatusSet:
		h.enqueue(clients, in.From, h.declareStatus(ctx, clients, in))
	case protocol.TypeAgentStatusGet:
		h.enqueue(clients, in.From, h.readStatus(ctx, clients, in))
	case protocol.TypeAgentActivity:
		h.noteActivity(in)
	}
}

// validRequest reports a status request with a usable request_id; activity
// reports carry none.
func validRequest(msg protocol.Message) bool {
	if msg.Type == protocol.TypeAgentActivity {
		return true
	}
	return msg.RequestID != "" && len(msg.RequestID) <= 128
}

func statusError(request, code, detail string) protocol.Message {
	return protocol.Message{Type: protocol.TypeError, RequestID: request, Code: code, Detail: detail}
}

// declareStatus validates, rate-limits and stores a declaration, then tells
// the operator. Nothing is confirmed or published unless it was stored.
func (h *Hub) declareStatus(ctx context.Context, clients map[string]*Client, in Inbound) protocol.Message {
	request := in.Msg.RequestID
	if in.Msg.AgentStatus == nil {
		return statusError(request, protocol.ErrInvalidStatus, "agent_status is required")
	}
	st := *in.Msg.AgentStatus
	if err := protocol.ValidateStatus(st); err != nil {
		return statusError(request, protocol.ErrInvalidStatus, err.Error())
	}
	now := time.Now().UTC()
	if !h.statuses.take(in.From.ID, now) {
		return statusError(request, protocol.ErrStatusRateLimited, "too many status updates; retry shortly")
	}
	st.AgentID, st.SessionID, st.DeclaredAt = in.From.ID, in.From.SessionID, now
	if err := h.store.SetAgentStatus(ctx, st); err != nil {
		h.log.Error("agent status not stored", "agent_id", in.From.ID, "error", err)
		return statusError(request, protocol.ErrInternal, "status not stored")
	}
	h.emit(protocol.SessionEvent{Kind: protocol.EventStatusChanged, AgentID: in.From.ID})
	h.present(clients, &st)
	return protocol.Message{Type: protocol.TypeAgentStatusResult, RequestID: request, AgentStatus: &st}
}

// readStatus returns an agent's latest record, or null; nothing is inferred.
func (h *Hub) readStatus(ctx context.Context, clients map[string]*Client, in Inbound) protocol.Message {
	agent := in.Msg.AgentID
	if agent == "" {
		agent = in.From.ID
	}
	st, err := h.store.AgentStatus(ctx, agent)
	if err != nil {
		h.log.Error("agent status not read", "agent_id", agent, "error", err)
		return statusError(in.Msg.RequestID, protocol.ErrInternal, "status unavailable")
	}
	h.present(clients, st)
	return protocol.Message{Type: protocol.TypeAgentStatusResult, RequestID: in.Msg.RequestID, AgentStatus: st}
}

// present fills the fields that describe the record now: whether its session
// is the agent's current one, and that session's observed activity.
func (h *Hub) present(clients map[string]*Client, st *protocol.AgentStatus) {
	if st == nil {
		return
	}
	c := clients[st.AgentID]
	st.Connected = c != nil && c.SessionID == st.SessionID
	st.Activity = nil
	if a, ok := h.statuses.activity[st.AgentID]; ok && a.session == st.SessionID {
		seen := a.seen
		st.Activity = &seen
	}
}

// noteActivity records activity observed for the sender's own session. The
// time is the broker's; the first report applies at once, later ones at most
// every activityCoalesce. Unknown sources are dropped.
func (h *Hub) noteActivity(in Inbound) {
	if !protocol.ValidActivitySource(in.Msg.Source) {
		h.log.Debug("agent activity from an unsupported source dropped", "agent_id", in.From.ID, "source", in.Msg.Source)
		return
	}
	now := time.Now().UTC()
	if h.statuses.recentActivity(in.From, now) {
		return
	}
	h.statuses.activity[in.From.ID] = sessionActivity{session: in.From.SessionID, seen: protocol.Activity{At: now, Source: in.Msg.Source}}
	h.emit(protocol.SessionEvent{Kind: protocol.EventStatusChanged, AgentID: in.From.ID})
}

// snapshotStatuses is the operator's view: the connected agents' records
// first, then retained offline ones by agent ID, at most SnapshotStatuses; the
// total counts every retained record so an omission stays visible.
func (h *Hub) snapshotStatuses(ctx context.Context, clients map[string]*Client) ([]protocol.AgentStatus, int, error) {
	h.statuses.init()
	current := make([]store.Current, 0, len(clients))
	for id, c := range clients {
		current = append(current, store.Current{AgentID: id, SessionID: c.SessionID})
	}
	sort.Slice(current, func(i, j int) bool { return current[i].AgentID < current[j].AgentID })
	page, err := h.store.AgentStatusPage(ctx, current, protocol.SnapshotStatuses)
	if err != nil {
		return nil, 0, err
	}
	for i := range page.Records {
		h.present(clients, &page.Records[i])
	}
	sort.SliceStable(page.Records, func(i, j int) bool { return page.Records[i].Connected && !page.Records[j].Connected })
	return page.Records, page.Total, nil
}
