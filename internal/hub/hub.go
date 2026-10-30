// Package hub implements the central routing goroutine: it owns the agent
// registry, assigns the global message sequence, persists, and fans out.
package hub

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"

	"github.com/google/uuid"
)

// Client represents one connected agent.
type Client struct {
	ID              string
	SessionID       string
	Harness         string
	Model           string
	ProtocolVersion int
	JoinRooms       []string              // v9 hello: rooms to join on registration
	Send            chan protocol.Message // buffered; sent to and closed only by the hub
	Disconnect      func()                // closes the transport; must return promptly
	connectedAt     time.Time
	lastSeenAt      time.Time
}

// Inbound is a message received from a client, tagged with its sender.
type Inbound struct {
	From *Client
	Msg  protocol.Message
}

// Hub routes messages between connected clients. All state mutations happen
// in the single Run goroutine, so no locking is required.
type Hub struct {
	membership chan membership
	inbound    chan Inbound
	importAt   int64       // where the next import pass goes on; owned by Run
	rescan     atomic.Bool // an import pass was asked for since the scan began
	importDue  atomic.Bool // an import pass is due; signalled on pair.changed (#222)
	store      *store.Store
	seq        uint64
	log        *slog.Logger
	done       chan struct{}
	status     chan statusRequest
	operator   chan operatorRequest
	startedAt  time.Time
	// Live operator session state, owned by the Run goroutine.
	owner       *OperatorSession
	epoch       string
	revision    uint64
	gen         uint64
	statuses    statusState
	pairing     *peer.Invitations       // operator pairing (#178); memory only
	identity    *peer.WorkspaceIdentity // loaded at Run's start or on a pairing request; never replaced
	pair        pairingWiring           // redemption and the inbound endpoint
	msgs        messageWiring           // message sessions with contacts (#210)
	live        liveWiring              // their live links (#222)
	reviewClock func() time.Time        // review lifecycle times (#201); nil is time.Now
}

// New creates a Hub. startSeq is the last persisted sequence number
// (from store.LastSeq), so the global order survives broker restarts.
func New(st *store.Store, startSeq uint64, log *slog.Logger) *Hub {
	h := &Hub{
		membership: make(chan membership),
		inbound:    make(chan Inbound, 64),
		store:      st,
		seq:        startSeq,
		log:        log,
		done:       make(chan struct{}),
		status:     make(chan statusRequest),
		operator:   make(chan operatorRequest),
		startedAt:  time.Now().UTC(),
		epoch:      uuid.NewString(),
	}
	h.pair.cfg = defaultPairingConfig()
	// The invitation manager does no I/O; it fails only on a missing store,
	// which leaves pairing unavailable rather than the hub.
	h.pairing, _ = peer.NewInvitations(peer.DefaultInvitationPolicy(), time.Now, rand.Reader, st)
	h.pair.changed = make(chan struct{}, 1)
	h.msgs.init(h.pair.changed)
	return h
}

// Register adds a client to the hub. Must be called before sending Inbound.
func (h *Hub) Register(ctx context.Context, c *Client) bool {
	select {
	case <-h.done:
		return false
	case <-ctx.Done():
		return false
	case h.membership <- membership{c, true}:
		return true
	}
}

// Unregister removes a client and closes its Send channel.
func (h *Hub) Unregister(c *Client) {
	select {
	case <-h.done:
	case h.membership <- membership{c, false}:
	}
}

// Submit hands a received message to the hub for routing.
func (h *Hub) Submit(ctx context.Context, in Inbound) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case <-h.done:
		return false
	default:
	}
	select {
	case <-h.done:
		return false
	case <-ctx.Done():
		return false
	case h.inbound <- in:
		return true
	}
}

// Done closes after all clients have been disconnected and their sessions recorded.
func (h *Hub) Done() <-chan struct{} { return h.done }

// Run processes hub events until ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	clients := make(map[string]*Client)
	defer close(h.done)
	defer h.settlePairing()
	defer h.disconnectAll(clients)
	h.beginPairing(ctx)
	h.drainImports(ctx, clients) // what was staged before a restart

	for {
		select {
		case <-ctx.Done():
			return

		case m := <-h.membership:
			h.member(clients, m)

		case in := <-h.inbound:
			h.route(ctx, clients, in)

		case req := <-h.status:
			req.reply <- h.snapshot(req.ctx, clients)

		case req := <-h.operator:
			h.handleOperator(req.ctx, clients, req)

		case <-h.pair.changed:
			h.background(ctx, clients)
		}
	}
}

// membership is a client joining or leaving the hub.
type membership struct {
	c      *Client
	joined bool
}

func (h *Hub) member(clients map[string]*Client, m membership) {
	if m.joined {
		h.connect(clients, m.c)
		return
	}
	h.disconnect(clients, m.c)
}

func (h *Hub) disconnectAll(clients map[string]*Client) {
	for _, c := range clients {
		h.disconnect(clients, c)
	}
}

type routingError struct {
	code   string
	detail string
}

// Error lets an operation return a routing error through an error result.
func (e *routingError) Error() string { return e.code + ": " + e.detail }

func (e *routingError) frame(id string) protocol.Message {
	return protocol.Message{Type: protocol.TypeError, Code: e.code, Detail: e.detail, MessageID: id}
}

// route validates a submission, freezes membership, then commits before delivery.
func (h *Hub) route(ctx context.Context, clients map[string]*Client, in Inbound) {
	if clients[in.From.ID] != in.From {
		return
	}
	in.From.lastSeenAt = time.Now().UTC()
	if h.routeControl(ctx, clients, in) {
		return
	}
	if err := h.prepareAgentSend(ctx, &in); err != nil {
		h.enqueue(clients, in.From, err.frame(in.Msg.MessageID))
		return
	}
	if !h.newSend(ctx, clients, in) {
		return
	}
	recipients, err := h.resolveRecipients(ctx, clients, in)
	if err != nil {
		h.enqueue(clients, in.From, err.frame(in.Msg.MessageID))
		return
	}
	out, err := h.persistRoute(ctx, in, recipients)
	if err != nil {
		h.enqueue(clients, in.From, err.frame(in.Msg.MessageID))
		return
	}
	h.noteRouted(ctx, out, recipients)
	if out.AckRequested {
		h.enqueue(clients, in.From, protocol.Message{Type: protocol.TypeAck, MessageID: out.MessageID, Stage: protocol.StageAccepted, Seq: out.Seq, Recipients: publicRecipients(recipients), Durable: out.Durable})
	}
	h.deliver(clients, in.From, out, recipients)
}

// newSend reports whether the message is a new send to route here: a reply
// to a paired workspace's message goes there instead, a retry gets its
// retained outcome, and a room post needs the sender's membership.
func (h *Hub) newSend(ctx context.Context, clients map[string]*Client, in Inbound) bool {
	if h.routeRemote(ctx, clients, in) {
		return false
	}
	if h.reportRetry(ctx, clients, in) {
		return false
	}
	if err := h.admitRoomPost(ctx, in); err != nil {
		h.enqueue(clients, in.From, err.frame(in.Msg.MessageID))
		return false
	}
	return true
}

// prepareAgentSend checks the envelope and decides the message's room.
func (h *Hub) prepareAgentSend(ctx context.Context, in *Inbound) *routingError {
	if err := prepareMessage(in); err != nil {
		return err
	}
	return h.scopeAgentSend(ctx, in)
}

func prepareMessage(in *Inbound) *routingError {
	for _, check := range agentEnvelopeChecks {
		if err := check(in.Msg, in.From.ProtocolVersion); err != nil {
			return err
		}
	}
	if in.Msg.MessageID == "" {
		in.Msg.MessageID = uuid.NewString()
	}
	return validateDurability(*in)
}

// agentEnvelopeChecks run in order before an ID is assigned or anything persists.
var agentEnvelopeChecks = []func(protocol.Message, int) *routingError{
	checkMsgType,
	func(msg protocol.Message, _ int) *routingError { return checkNoOrigin(msg) },
	checkIDLengths,
	checkAckCapability,
	checkAgentAddress,
	checkPublicFlags,
	func(msg protocol.Message, _ int) *routingError { return checkNoMentions(msg) },
	checkWorkspace,
}

// checkWorkspace admits a message naming a paired workspace from v13, to one
// agent there or its operator.
func checkWorkspace(msg protocol.Message, version int) *routingError {
	switch {
	case msg.Workspace == "":
		return checkNoAsk(msg)
	case version < protocol.WorkspaceVersion:
		return &routingError{protocol.ErrMalformedFrame, "workspace requires protocol_version 13"}
	case msg.To == protocol.Broadcast || msg.Room != "":
		return &routingError{protocol.ErrMalformedFrame, "a message to a paired workspace goes to one agent there or its operator"}
	case len(msg.Workspace) > 128:
		return &routingError{protocol.ErrMalformedFrame, "workspace is a contact's key or name, at most 128 bytes"}
	}
	return checkAsk(msg, version)
}

// checkNoAsk: a review request goes to a paired workspace.
func checkNoAsk(msg protocol.Message) *routingError {
	if msg.ReviewAsk != nil {
		return &routingError{protocol.ErrMalformedFrame, "review_ask goes with workspace"}
	}
	return nil
}

// checkAsk admits a review request from v14: new, to one agent there.
func checkAsk(msg protocol.Message, version int) *routingError {
	switch {
	case msg.ReviewAsk == nil:
		return nil
	case version < protocol.ReviewAskVersion:
		return &routingError{protocol.ErrMalformedFrame, "review_ask requires protocol_version 14"}
	case msg.InReplyTo != "" || msg.To == protocol.OperatorID:
		return &routingError{protocol.ErrMalformedFrame, "a review request asks one agent of the workspace and answers nothing"}
	}
	return nil
}

// checkNoMentions: mentions are the operator's for now (#164).
func checkNoMentions(msg protocol.Message) *routingError {
	if len(msg.Mentions) != 0 {
		return &routingError{protocol.ErrMalformedFrame, "mentions are operator-only"}
	}
	return nil
}

// checkPublicFlags requires durable delivery with acknowledgments for a v7
// public broadcast: its operator copy has no other delivery mode.
func checkPublicFlags(msg protocol.Message, version int) *routingError {
	if !isPublic(msg.To, version) {
		return nil
	}
	if durableWithAcks(msg) {
		return nil
	}
	return &routingError{protocol.ErrMalformedFrame, "a public broadcast (to \"*\", protocol_version 7) must be durable with ack_requested"}
}

func durableWithAcks(msg protocol.Message) bool {
	return msg.Durable && msg.AckRequested
}

// isPublic reports whether "*" from this protocol version is the shared
// conversation (connected peers and the operator inbox). Older versions keep
// the agent-only broadcast.
func isPublic(to string, version int) bool {
	return to == protocol.Broadcast && version >= protocol.PublicBroadcastVersion
}

func checkMsgType(msg protocol.Message, _ int) *routingError {
	if msg.Type != protocol.TypeMsg {
		return &routingError{protocol.ErrMalformedFrame, "expected type msg or ack"}
	}
	return nil
}

func checkIDLengths(msg protocol.Message, _ int) *routingError {
	if len(msg.MessageID) > 128 || len(msg.InReplyTo) > 128 {
		return &routingError{protocol.ErrMalformedFrame, "message_id and in_reply_to must be at most 128 bytes"}
	}
	return nil
}

func checkAckCapability(msg protocol.Message, version int) *routingError {
	if msg.AckRequested && version < protocol.AcknowledgmentVersion {
		return &routingError{protocol.ErrMalformedFrame, "ack_requested requires protocol_version 2 in hello"}
	}
	return nil
}

// checkAgentAddress keeps operator-only addressing out of agent sends.
func checkAgentAddress(msg protocol.Message, version int) *routingError {
	switch {
	case msg.To == "":
		return &routingError{protocol.ErrMissingRecipient, "field to is required"}
	case len(msg.Group) != 0 || len(msg.Excluded) != 0:
		return &routingError{protocol.ErrMalformedFrame, "group sends are operator-only"}
	case msg.To == protocol.OperatorID:
		return checkOperatorAddress(msg, version)
	}
	return nil
}

// checkOperatorAddress admits durable messages to the operator inbox from agents
// that negotiated the operator inbox protocol.
func checkOperatorAddress(msg protocol.Message, version int) *routingError {
	durable := msg.Durable && msg.AckRequested
	switch {
	case version < protocol.OperatorInboxVersion && !durable:
		return &routingError{protocol.ErrUnknownRecipient, "the operator inbox accepts only durable messages from protocol_version 5"}
	case version < protocol.OperatorInboxVersion:
		return &routingError{protocol.ErrDurabilityUnavailable, "messages to the operator require protocol_version 5"}
	case !durable:
		return &routingError{protocol.ErrMalformedFrame, "messages to the operator must be durable with ack_requested"}
	}
	return nil
}

// broadcastRecipients is every connected agent the audience admits, except
// the sender.
func broadcastRecipients(clients map[string]*Client, sender *Client, admit func(*Client) bool) ([]protocol.Recipient, *routingError) {
	var recipients []protocol.Recipient
	for _, c := range clients {
		if c != sender && admit(c) {
			recipients = append(recipients, c.identity())
		}
	}
	if len(recipients) > protocol.MaxRecipients {
		return nil, &routingError{protocol.ErrRecipientUnavailable, "broadcast exceeds 256 connected recipients; send to smaller groups directly"}
	}
	sort.Slice(recipients, func(i, j int) bool { return recipients[i].AgentID < recipients[j].AgentID })
	return recipients, nil
}

func (c *Client) identity() protocol.Recipient {
	return protocol.Recipient{AgentID: c.ID, SessionID: c.SessionID}
}

func (h *Hub) persistRoute(ctx context.Context, in Inbound, recipients []protocol.Recipient) (protocol.Message, *routingError) {
	h.seq++
	ts := time.Now().UTC()
	msg := in.Msg
	out := protocol.Message{
		Type: protocol.TypeMsg, Seq: h.seq, TS: &ts,
		From: in.From.ID, SessionID: in.From.SessionID, To: msg.To,
		Payload: msg.Payload, MessageID: msg.MessageID, Origin: protocol.OriginAgent,
		InReplyTo: msg.InReplyTo, AckRequested: msg.AckRequested, Durable: msg.Durable, Room: msg.Room,
	}
	// Added broker metadata must still fit the receiver's frame limit.
	sized := out
	sized.Replayed = out.Durable
	encoded, err := json.Marshal(sized)
	if err != nil || len(encoded)+1 > protocol.MaxFrameBytes {
		return out, &routingError{protocol.ErrMalformedFrame, "routed message exceeds the 1 MiB frame limit"}
	}
	if err := h.store.PersistMessage(ctx, out, in.From.SessionID, recipients); err != nil {
		return out, h.refusedSend(ctx, in, recipients, err)
	}
	return out, nil
}

// refusedSend maps a persistence failure; when an inbox bound refused a
// message to the operator, the refusal is audited (payload-free) and then
// the operator session learns which bound, since only the operator can free
// it. Without the audit row, no event is raised.
func (h *Hub) refusedSend(ctx context.Context, in Inbound, recipients []protocol.Recipient, err error) *routingError {
	rerr := h.persistenceError(in.Msg.MessageID, err)
	var full *store.InboxFullError
	if !errors.As(err, &full) || !addressesOperator(recipients) {
		return rerr
	}
	audit := store.OperatorAudit{Action: "send_refused", MessageID: in.Msg.MessageID, Scope: "from=" + in.From.ID, Result: store.AuditRejected, Detail: full.Detail()}
	if _, aerr := h.store.RecordOperatorAudit(ctx, audit); aerr != nil {
		h.log.Error("refusal audit failed; operator not notified", "message_id", in.Msg.MessageID, "error", aerr)
		return rerr
	}
	h.emit(protocol.SessionEvent{Kind: protocol.EventSendRefused, AgentID: in.From.ID, Detail: full.Detail()})
	return rerr
}

func addressesOperator(recipients []protocol.Recipient) bool {
	for _, r := range recipients {
		if r.AgentID == protocol.OperatorID {
			return true
		}
	}
	return false
}

func (h *Hub) persistenceError(id string, err error) *routingError {
	if errors.Is(err, store.ErrInboxFull) {
		return &routingError{protocol.ErrInboxFull, err.Error()}
	}
	if errors.Is(err, store.ErrDuplicateMessage) {
		return &routingError{protocol.ErrDuplicateMessage, err.Error()}
	}
	h.log.Error("persist message failed", "message_id", id, "error", err)
	return &routingError{protocol.ErrInternal, "message not persisted"}
}

// sessionOwner also prevents acknowledgments reaching a replacement sender.
func sessionOwner(clients map[string]*Client, identity protocol.Recipient) *Client {
	c := clients[identity.AgentID]
	if c == nil {
		return nil
	}
	if c.SessionID != identity.SessionID {
		return nil
	}
	return c
}

func (h *Hub) deliver(clients map[string]*Client, sender *Client, msg protocol.Message, recipients []protocol.Recipient) {
	for _, r := range recipients {
		c := sessionOwner(clients, r)
		if c == nil {
			if !msg.Durable {
				h.reportUnavailable(clients, sender, msg, r)
			}
			continue
		}
		if !h.enqueue(clients, c, msg) {
			h.reportUnavailable(clients, sender, msg, r)
		}
	}
}

func (h *Hub) reportUnavailable(clients map[string]*Client, sender *Client, msg protocol.Message, recipient protocol.Recipient) {
	failure := routingError{protocol.ErrRecipientUnavailable, "recipient " + recipient.AgentID + " session " + recipient.SessionID + " disconnected before enqueue"}
	if msg.Durable {
		failure.detail += "; pending message retained for replay"
	}
	h.enqueue(clients, sender, failure.frame(msg.MessageID))
}

// checkNoOrigin rejects a claimed origin: the broker assigns it from the connection.
func checkNoOrigin(msg protocol.Message) *routingError {
	if msg.Origin != "" {
		return &routingError{protocol.ErrMalformedFrame, "origin is set by the broker and cannot be supplied by agents"}
	}
	return nil
}

func (h *Hub) acknowledge(ctx context.Context, clients map[string]*Client, in Inbound) {
	msg := in.Msg
	if err := checkNoOrigin(msg); err != nil {
		h.enqueue(clients, in.From, err.frame(msg.MessageID))
		return
	}
	report, err := h.store.AcknowledgeReport(ctx, store.Acknowledgment{MessageID: msg.MessageID, Recipient: in.From.identity(), Stage: msg.Stage, ProtocolVersion: in.From.ProtocolVersion})
	sender := report.Sender
	if err != nil {
		code, detail := protocol.ErrInternal, "acknowledgment not persisted"
		if errors.Is(err, store.ErrInvalidAck) {
			code, detail = protocol.ErrInvalidAck, err.Error()
		}
		h.sendError(clients, in.From, code, detail, msg.MessageID)
		return
	}
	h.noteAck(in, report)
	h.remoteAcknowledged(msg, report)
	out := protocol.Message{Type: protocol.TypeAck, MessageID: msg.MessageID, Stage: msg.Stage, AgentID: in.From.ID, SessionID: in.From.SessionID}
	// Adapter receipts are confirmed to the sender. Explicit agent acknowledgments
	// are also confirmed to the acknowledging agent, only after the database commit.
	if msg.Stage == protocol.StageAgentAcknowledged {
		h.enqueue(clients, in.From, out)
	}
	c := sessionOwner(clients, sender)
	if c == nil {
		return
	}
	if c == in.From && msg.Stage == protocol.StageAgentAcknowledged {
		return
	}
	h.enqueue(clients, c, out)
}

func (h *Hub) sendError(clients map[string]*Client, c *Client, code, detail, id string) {
	h.enqueue(clients, c, protocol.Message{Type: protocol.TypeError, Code: code, Detail: detail, MessageID: id})
}

// enqueue evicts slow clients instead of blocking routing for every agent.
func (h *Hub) enqueue(clients map[string]*Client, c *Client, msg protocol.Message) bool {
	if clients[c.ID] != c {
		return false
	}
	select {
	case c.Send <- msg:
		return true
	default:
		h.log.Warn("disconnecting slow reader", "agent_id", c.ID)
		h.disconnect(clients, c)
		return false
	}
}

func (h *Hub) disconnect(clients map[string]*Client, c *Client) {
	if clients[c.ID] != c {
		return
	}
	delete(clients, c.ID)
	if c.Disconnect != nil {
		c.Disconnect()
	}
	close(c.Send)
	h.statuses.forget(c)
	h.recordDisconnect(c)
	h.notePresence(c, "disconnected")
	h.notifyDisconnect(clients, c)
	h.log.Info("agent disconnected", "agent_id", c.ID, "online", len(clients))
}

func (h *Hub) notifyDisconnect(clients map[string]*Client, c *Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var after uint64
	for len(clients) > 0 {
		pending, err := h.store.UnacknowledgedForSession(ctx, c.SessionID, after)
		if err != nil {
			h.reportReceiptQueryFailure(clients, c, err)
			return
		}
		h.notifyPendingDisconnects(clients, c, pending)
		if len(pending) < 64 {
			return
		}
		after = pending[len(pending)-1].Seq
	}
}

func (h *Hub) reportReceiptQueryFailure(clients map[string]*Client, c *Client, err error) {
	h.log.Error("query unacknowledged receipts failed", "session_id", c.SessionID, "error", err)
	// Expose failure to enumerate affected messages without inventing an outcome.
	for _, sender := range clients {
		h.enqueue(clients, sender, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrInternal, AgentID: c.ID, SessionID: c.SessionID, Detail: "recipient disconnected; could not determine unacknowledged messages"})
	}
}

func (h *Hub) notifyPendingDisconnects(clients map[string]*Client, c *Client, pending []store.Unacknowledged) {
	for _, p := range pending {
		sender := sessionOwner(clients, protocol.Recipient{AgentID: p.Sender, SessionID: p.SenderSession})
		if sender != nil {
			h.enqueue(clients, sender, protocol.Message{Type: protocol.TypeError, Code: protocol.ErrRecipientDisconnected, MessageID: p.MessageID, AgentID: c.ID, SessionID: c.SessionID, Detail: disconnectDetail(p.Durable)})
		}
	}
}

func (h *Hub) recordConnect(c *Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.connectedAt = time.Now().UTC()
	c.lastSeenAt = c.connectedAt
	if err := h.store.RecordConnect(ctx, store.SessionRecord{Identity: c.identity(), Harness: c.Harness, Model: c.Model, ConnectedAt: c.connectedAt}); err != nil {
		h.log.Error("record connect failed", "agent_id", c.ID, "error", err)
		return err
	}
	return nil
}

func (h *Hub) recordDisconnect(c *Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.store.RecordDisconnect(ctx, c.SessionID, time.Now()); err != nil {
		h.log.Error("record disconnect failed", "agent_id", c.ID, "error", err)
	}
}

// routeControl handles the frames that are not messages: acknowledgments and
// agent status. It reports whether it handled the frame.
func (h *Hub) routeControl(ctx context.Context, clients map[string]*Client, in Inbound) bool {
	switch {
	case isStatusFrame(in.Msg.Type):
		h.handleStatus(ctx, clients, in)
	case claimFrames[in.Msg.Type]:
		h.handleClaim(ctx, clients, in)
	case roomFrames[in.Msg.Type]:
		h.handleRoom(ctx, clients, in)
	case reviewFrames[in.Msg.Type]:
		h.handleReview(ctx, clients, in)
	case operatorOnly[in.Msg.Type] != "":
		h.enqueue(clients, in.From, operatorOnlyRefusal(in.Msg))
	case in.Msg.Type == protocol.TypeAck:
		h.acknowledge(ctx, clients, in)
	case in.Msg.Type == protocol.TypeWorkspacesGet:
		h.enqueue(clients, in.From, h.workspaces(ctx, in))
	default:
		return false
	}
	return true
}

// operatorOnly are the operator frames an agent may send by mistake: each
// gets a correlated refusal before any lookup, audit or change, and nothing
// about what it names. Reviews (#201), pairing (#178) and contact messages
// (#210) are operator-socket only.
var operatorOnly = map[string]string{
	protocol.TypeOperatorReview:  "review requests are operator-only",
	protocol.TypeOperatorPairing: "pairing is operator-only",
	protocol.TypeOperatorContact: "contact messages are operator-only",
}

func operatorOnlyRefusal(msg protocol.Message) protocol.Message {
	refusal := statusError(msg.RequestID, protocol.ErrForbidden, operatorOnly[msg.Type])
	refusal.MessageID = msg.MessageID
	return refusal
}

func isStatusFrame(kind string) bool {
	switch kind {
	case protocol.TypeAgentStatusSet, protocol.TypeAgentStatusGet, protocol.TypeAgentActivity:
		return true
	}
	return false
}
