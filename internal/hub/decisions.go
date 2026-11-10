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
// records the request and queues its notice. A keyed replay returns the
// standing record before anything is posted again.
func (h *Hub) decide(ctx context.Context, clients map[string]*Client, in Inbound) protocol.Message {
	msg := in.Msg
	if rerr := admitDecision(in); rerr != nil {
		return statusError(msg.RequestID, rerr.code, rerr.detail)
	}
	standing, err := h.store.DecisionByKey(ctx, in.From.ID, msg.DecisionKey)
	if err != nil {
		return statusError(msg.RequestID, protocol.ErrInternal, "decision lookup failed")
	}
	if standing != nil {
		return decisionResult(msg.RequestID, *standing, true)
	}
	anchor, rerr := h.decisionAnchor(ctx, clients, in)
	if rerr != nil {
		return statusError(msg.RequestID, rerr.code, rerr.detail)
	}
	return h.recordDecision(ctx, in, anchor)
}

// admitDecision is the version gate and the request's validation.
func admitDecision(in Inbound) *routingError {
	if in.From.ProtocolVersion < protocol.DecisionVersion {
		return &routingError{protocol.ErrForbidden, "decision requests need protocol version 27"}
	}
	if err := protocol.ValidateDecisionRequest(in.Msg.DecisionSummary, in.Msg.DecisionKey); err != nil {
		return &routingError{protocol.ErrMalformedFrame, err.Error()}
	}
	return nil
}

// recordDecision stores the request behind its anchor and queues its
// notice when there is a notifier; an anchor already recorded is replayed.
func (h *Hub) recordDecision(ctx context.Context, in Inbound, anchor string) protocol.Message {
	row := store.Decision{ID: "d-" + uuid.NewString(), Agent: in.From.ID, Key: in.Msg.DecisionKey, Anchor: anchor,
		Summary: in.Msg.DecisionSummary, Delivery: protocol.DeliveryOff, Reason: "no_notifier"}
	if h.NotifierEnabled() {
		row.Delivery, row.Reason = protocol.DeliveryPending, ""
	}
	created, existing, err := h.store.CreateDecision(ctx, row)
	if err != nil {
		return statusError(in.Msg.RequestID, protocol.ErrInternal, "decision not recorded")
	}
	if !existing && created.Delivery == protocol.DeliveryPending {
		h.queueDecision(created.ID)
	}
	return decisionResult(in.Msg.RequestID, created, existing)
}

func decisionResult(requestID string, d store.Decision, replayed bool) protocol.Message {
	view := d.View(replayed)
	return protocol.Message{Type: protocol.TypeDecisionResult, RequestID: requestID, Decision: &view}
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
	id := anchorID(in)
	payload, _ := json.Marshal(map[string]string{"text": "needs a decision: " + in.Msg.DecisionSummary}) // a map of strings always encodes
	ask := Inbound{From: in.From, Msg: protocol.Message{Type: protocol.TypeMsg, To: protocol.OperatorID, Payload: payload,
		MessageID: id, Durable: true, AckRequested: true}}
	if rerr := h.sendAsAgent(ctx, clients, ask); rerr != nil {
		return "", rerr
	}
	return id, nil
}

// anchorID: fixed by (agent, key) when there is a key, random otherwise.
func anchorID(in Inbound) string {
	if in.Msg.DecisionKey == "" {
		return "d-" + uuid.NewString()
	}
	sum := sha256.Sum256([]byte(in.From.ID + "\x00" + in.Msg.DecisionKey))
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

// drainPending posts every pending request the store holds, a page at a
// time until a page comes back short; a request that stays pending (a
// post interrupted by shutdown) ends the pass rather than loop on it.
func (h *Hub) drainPending(ctx context.Context) {
	for ctx.Err() == nil {
		pending, err := h.store.PendingDecisions(ctx, DecisionQueue)
		if err != nil {
			h.log.Error("decision requests unavailable", "error", err)
			return
		}
		h.notifyEach(ctx, pending)
		if len(pending) < DecisionQueue {
			return
		}
	}
}

func (h *Hub) notifyEach(ctx context.Context, pending []store.Decision) {
	for _, d := range pending {
		if ctx.Err() != nil {
			return
		}
		h.notifyDecision(ctx, d.ID)
	}
}

// notifyDecision posts one request's notice, with bounded retries; the
// outcome is recorded whatever happened, and the agent is never waited for.
// Each attempt starts from the row as it stands: an answer given during a
// backoff ends the retries.
func (h *Hub) notifyDecision(ctx context.Context, id string) {
	for attempt := 0; ; attempt++ {
		d := h.openPending(ctx, id)
		if d == nil {
			return // gone, sent meanwhile, or answered first
		}
		outcome := h.post(ctx, *d, attempt)
		h.recordDelivery(ctx, outcome)
		if outcome.Delivery != protocol.DeliveryPending {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-h.notifier.cfg.After(decisionBackoff[attempt]):
		}
	}
}

// post makes one attempt and says what it left: sent, failed, or pending
// with the failure's class when a later attempt may do better.
func (h *Hub) post(ctx context.Context, d store.Decision, attempt int) store.DeliveryMark {
	c := h.notifier.cfg
	mark := store.DeliveryMark{ID: d.ID, Delivery: protocol.DeliverySent, At: c.Clock()}
	_, err := c.Post(ctx, notify.Notice{Workspace: c.Workspace, Agent: d.Agent, Summary: d.Summary, Anchor: d.Anchor, Hidden: c.Hidden})
	if err == nil {
		return mark
	}
	mark.Delivery, mark.Reason = protocol.DeliveryFailed, failureClass(err)
	if attempt < len(decisionBackoff) && retryable(err) {
		mark.Delivery = protocol.DeliveryPending
	}
	return mark
}

// openPending is the request when its notice is still owed, else nil.
func (h *Hub) openPending(ctx context.Context, id string) *store.Decision {
	d, err := h.store.Decision(ctx, id)
	if err != nil || d == nil {
		return nil
	}
	if !d.Owed() {
		return nil
	}
	return d
}

// retryable: a later attempt may succeed.
func retryable(err error) bool {
	var failure *notify.Failure
	return errors.As(err, &failure) && failure.Retryable()
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

func (h *Hub) recordDelivery(ctx context.Context, mark store.DeliveryMark) {
	if err := h.store.MarkDecisionDelivery(ctx, mark); err != nil {
		h.log.Error("decision delivery not recorded", "id", mark.ID, "error", err)
	}
	h.observe(mark.ID)
}

// observe tells the test hook, when there is one, that a request changed.
func (h *Hub) observe(id string) {
	if c := h.notifier.cfg; c != nil && c.Observed != nil {
		c.Observed(id)
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
	if answered {
		h.observe(reply.InReplyTo)
	}
}
