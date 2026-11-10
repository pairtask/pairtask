package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/notify"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Decision requests (#352): an agent asks the broker to record that it
// needs the operator's decision and to tell the operator outside the chat.
// The request hangs on an anchor, a durable message from the agent to the
// operator; the broker posts that message itself when the request names
// none, so every anchor is a real delivered chat message. The operator's
// reply to the anchor answers the request; nothing else does. A notifier,
// when configured, carries one notice per request; delivery is never a
// decision.

// DecisionQueue bounds the notices waiting for the worker; the rest stay
// pending in the store and are taken up on the next scan.
const DecisionQueue = 64

// Notifier is what the hub is given when notifications are on. Post sends
// one notice; the hooks let tests drive time.
type Notifier struct {
	Workspace string
	Hidden    bool
	Post      func(ctx context.Context, notice notify.Notice) (receipt string, err error)
	After     func(time.Duration) <-chan time.Time
	Clock     func() time.Time
	Observed  func(id string)
}

type hubNotifier struct {
	cfg     *Notifier
	jobs    chan string // decision ids
	wake    chan struct{}
	workers sync.WaitGroup
}

var decisionBackoff = []time.Duration{2 * time.Second, 8 * time.Second}

// SetNotifier turns notifications on; call before Run. Nil disables: requests
// are still recorded, with delivery off.
func (h *Hub) SetNotifier(cfg *Notifier) {
	if cfg == nil || cfg.Post == nil {
		h.notifier = hubNotifier{}
		return
	}
	if cfg.After == nil {
		cfg.After = time.After
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	h.notifier = hubNotifier{cfg: cfg, jobs: make(chan string, DecisionQueue), wake: make(chan struct{}, 1)}
}

// NotifierEnabled: notices are posted.
func (h *Hub) NotifierEnabled() bool { return h.notifier.cfg != nil }

var decisionFrames = map[string]bool{protocol.TypeDecisionRequest: true}

// handleDecision serves one request frame from an agent.
func (h *Hub) handleDecision(ctx context.Context, clients map[string]*Client, in Inbound) {
	h.enqueue(clients, in.From, h.decide(ctx, clients, in))
}

// decide validates the request, posts the anchor when none is named,
// records the request and queues its notice.
func (h *Hub) decide(ctx context.Context, clients map[string]*Client, in Inbound) protocol.Message {
	msg := in.Msg
	if in.From.ProtocolVersion < protocol.DecisionVersion {
		return statusError(msg.RequestID, protocol.ErrForbidden, "decision requests need protocol version 26")
	}
	if err := protocol.ValidateDecisionRequest(msg.DecisionSummary, msg.DecisionKey); err != nil {
		return statusError(msg.RequestID, protocol.ErrMalformedFrame, err.Error())
	}
	anchor, rerr := h.decisionAnchor(ctx, clients, in)
	if rerr != nil {
		return statusError(msg.RequestID, rerr.code, rerr.detail)
	}
	delivery, reason := protocol.DeliveryOff, "no_notifier"
	if h.NotifierEnabled() {
		delivery, reason = protocol.DeliveryPending, ""
	}
	created, existing, err := h.store.CreateDecision(ctx, store.Decision{ID: "d-" + uuid.NewString(), Agent: in.From.ID,
		Key: msg.DecisionKey, Anchor: anchor, Summary: msg.DecisionSummary, Delivery: delivery, Reason: reason})
	if err != nil {
		return statusError(msg.RequestID, protocol.ErrInternal, "decision not recorded")
	}
	if !existing && delivery == protocol.DeliveryPending {
		h.queueDecision(created.ID)
	}
	return protocol.Message{Type: protocol.TypeDecisionResult, RequestID: msg.RequestID, Decision: decisionView(created, existing)}
}

func decisionView(d store.Decision, replayed bool) *protocol.Decision {
	view := d.View(replayed)
	return &view
}

// decisionAnchor is the request's anchor: the message it names, which must
// be a durable message from this agent to the operator, or a new one the
// broker posts as the agent, with an id fixed by the agent's key so a
// replay posts nothing twice.
func (h *Hub) decisionAnchor(ctx context.Context, clients map[string]*Client, in Inbound) (string, *routingError) {
	if in.Msg.AnchorMessageID != "" {
		owned, err := h.store.AnchorOwnedBy(ctx, in.Msg.AnchorMessageID, in.From.ID)
		if err != nil {
			return "", &routingError{protocol.ErrInternal, "anchor lookup failed"}
		}
		if !owned {
			return "", &routingError{protocol.ErrDecisionAnchor, "the anchor must be a durable message you sent to the operator"}
		}
		return in.Msg.AnchorMessageID, nil
	}
	id := anchorID(in.From.ID, in.Msg.DecisionKey)
	payload, _ := json.Marshal(map[string]string{"text": "needs a decision: " + in.Msg.DecisionSummary}) // a map of strings always encodes
	ask := Inbound{From: in.From, Msg: protocol.Message{Type: protocol.TypeMsg, To: protocol.OperatorID, Payload: payload,
		MessageID: id, Durable: true, AckRequested: true}}
	if rerr := h.sendAsAgent(ctx, clients, ask); rerr != nil {
		return "", rerr
	}
	return id, nil
}

// anchorID: fixed by (agent, key) when there is a key, random otherwise.
func anchorID(agent, key string) string {
	if key == "" {
		return "d-" + uuid.NewString()
	}
	sum := sha256.Sum256([]byte(agent + "\x00" + key))
	return "d-" + hex.EncodeToString(sum[:16])
}

// queueDecision hands a recorded request to the worker; a full queue leaves
// it pending in the store for the next scan.
func (h *Hub) queueDecision(id string) {
	select {
	case h.notifier.jobs <- id:
	default:
		h.wakeNotifier()
	}
}

func (h *Hub) wakeNotifier() {
	select {
	case h.notifier.wake <- struct{}{}:
	default:
	}
}

// startNotifier runs the worker until ctx ends; it takes up what was
// pending before it started, and again whenever woken.
func (h *Hub) startNotifier(ctx context.Context) {
	if h.notifier.cfg == nil {
		return
	}
	h.notifier.workers.Add(1)
	go func() {
		defer h.notifier.workers.Done()
		h.drainPending(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case id := <-h.notifier.jobs:
				h.notifyDecision(ctx, id)
			case <-h.notifier.wake:
				h.drainPending(ctx)
			}
		}
	}()
}

// settleNotifier waits for the worker at shutdown.
func (h *Hub) settleNotifier() { h.notifier.workers.Wait() }

// drainPending posts every pending request the store holds.
func (h *Hub) drainPending(ctx context.Context) {
	pending, err := h.store.PendingDecisions(ctx, DecisionQueue)
	if err != nil {
		h.log.Error("decision requests unavailable", "error", err)
		return
	}
	for _, d := range pending {
		if ctx.Err() != nil {
			return
		}
		h.notifyDecision(ctx, d.ID)
	}
}

// notifyDecision posts one request's notice, with bounded retries; the
// outcome is recorded whatever happened, and the agent is never waited for.
func (h *Hub) notifyDecision(ctx context.Context, id string) {
	c := h.notifier.cfg
	d, err := h.store.Decision(ctx, id)
	if err != nil || d == nil || d.Delivery != protocol.DeliveryPending || d.State != protocol.DecisionOpen {
		return // gone, sent meanwhile, or answered first
	}
	notice := notify.Notice{Workspace: c.Workspace, Agent: d.Agent, Summary: d.Summary, Anchor: d.Anchor, Hidden: c.Hidden}
	for attempt := 0; ; attempt++ {
		_, err := c.Post(ctx, notice)
		if err == nil {
			h.recordDelivery(ctx, id, protocol.DeliverySent, "")
			return
		}
		class := failureClass(err)
		var failure *notify.Failure
		if attempt >= len(decisionBackoff) || !(errors.As(err, &failure) && failure.Retryable()) {
			h.recordDelivery(ctx, id, protocol.DeliveryFailed, class)
			return
		}
		select {
		case <-ctx.Done():
			h.recordDelivery(ctx, id, protocol.DeliveryPending, class)
			return
		case <-c.After(decisionBackoff[attempt]):
		}
	}
}

// failureClass names a failed post without the transport's words, which
// could carry the webhook's secret path.
func failureClass(err error) string {
	var failure *notify.Failure
	if errors.As(err, &failure) {
		return failure.Class
	}
	return "request"
}

func (h *Hub) recordDelivery(ctx context.Context, id, delivery, reason string) {
	if err := h.store.MarkDecisionDelivery(ctx, id, delivery, reason, h.notifier.cfg.Clock()); err != nil {
		h.log.Error("decision delivery not recorded", "id", id, "error", err)
	}
	if observed := h.notifier.cfg.Observed; observed != nil {
		observed(id)
	}
}

// answerDecision closes the request an operator reply answers: the reply's
// in_reply_to is the anchor. Called once the reply is persisted.
func (h *Hub) answerDecision(ctx context.Context, reply protocol.Message) {
	if reply.InReplyTo == "" {
		return
	}
	answered, err := h.store.MarkDecisionAnswered(ctx, reply.InReplyTo, time.Now())
	if err != nil {
		h.log.Error("decision answer not recorded", "anchor", reply.InReplyTo, "error", err)
		return
	}
	if answered && h.notifier.cfg != nil && h.notifier.cfg.Observed != nil {
		h.notifier.cfg.Observed(reply.InReplyTo)
	}
}
