package hub

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
	"github.com/pairtask/pairtask/internal/store"
)

// Messages between this side's agents and a paired workspace (#222). A
// message from the contact to agents is staged when its session stores it;
// the loop imports it into their durable inboxes, since it owns the
// sequence, and hands it to those connected at v12. An agent's reply to one
// goes back to the contact, and its acknowledgment too.

// importBatch is how many staged messages one page holds; importPass how
// many one pass tries, so ones a full inbox holds back never keep the
// others waiting.
const (
	importBatch = 64
	importPass  = 1024
)

// importsDue asks the loop for an import pass, after something that may
// let a held-back message in: a scan going on past it will start again
// from the oldest. Safe from any goroutine; it never blocks.
func (h *Hub) importsDue() {
	h.rescan.Store(true)
	h.importPass()
}

// importPass asks the loop for the next pass, which goes on with a scan.
// It rides on the background signal.
func (h *Hub) importPass() {
	h.importDue.Store(true)
	select {
	case h.pair.changed <- struct{}{}:
	default: // one is due already
	}
}

// background is the loop's work behind the signal: announcements, and an
// import pass when one is due.
func (h *Hub) background(ctx context.Context, clients map[string]*Client) {
	h.announceBackground()
	if h.importDue.Swap(false) {
		h.drainImports(ctx, clients)
	}
}

// drainImports imports what is staged, oldest first, a bounded pass at a
// time: a pass goes on from where the last one stopped, so ones a full
// inbox holds back never hide later ones, and asks for the next until the
// scan reaches the end. Those still held back wait for an acknowledgment,
// a round or a grant to start a scan again, even one asked for mid-scan.
func (h *Hub) drainImports(ctx context.Context, clients map[string]*Client) {
	if h.store == nil {
		return // a hub without storage stages nothing
	}
	if h.importAt == 0 {
		h.rescan.Store(false) // this scan sees everything staged before it
	}
	for tried := 0; tried < importPass; tried += importBatch {
		if h.importPage(ctx, clients) {
			return
		}
	}
	h.importPass()
}

// importPage imports one page of the scan; done when the pass has nothing
// further to do.
func (h *Hub) importPage(ctx context.Context, clients map[string]*Client) bool {
	pending, err := h.store.PendingImportsAfter(ctx, h.importAt, importBatch)
	if err != nil {
		h.log.Error("staged messages not read", "error", err)
		return true
	}
	for _, imp := range pending {
		h.importOne(ctx, clients, imp)
		h.importAt = imp.Order
	}
	if len(pending) == importBatch {
		return false
	}
	h.importAt = 0 // the scan reached the end
	if h.rescan.Load() {
		h.importPass()
	}
	return true
}

// importOne imports one staged message at the next sequence, which only an
// import uses, and hands it to its recipients connected now.
func (h *Hub) importOne(ctx context.Context, clients map[string]*Client, imp store.RemoteImport) {
	imp.Sessions = remoteSessions(clients, imp.Recipients)
	result, err := h.store.ImportRemote(ctx, imp, h.seq+1, time.Now())
	if err != nil {
		h.importHeld(err)
		return
	}
	if result.Changed && result.State == store.ImportImported {
		h.seq++
		h.deliverImport(clients, result)
	}
	if result.ReviewID != "" {
		h.emit(protocol.SessionEvent{Kind: protocol.EventReviewsChanged, ReviewID: result.ReviewID})
	}
	if result.Changed && imp.IsReview() {
		h.SyncMessages(imp.Contact) // the request's outcome leaves at once
	}
}

// importHeld logs why an import waits, unless a full inbox or taken IDs
// hold it, which a later pass retries.
func (h *Hub) importHeld(err error) {
	if slices.ContainsFunc(retriedImports, func(r error) bool { return errors.Is(err, r) }) {
		return
	}
	h.log.Error("staged message not imported", "error", err)
}

// retriedImports hold an import back for a later pass: a full inbox, IDs
// all taken, or the open reviews at their bound.
var retriedImports = []error{store.ErrInboxFull, store.ErrLocalIDsTaken, review.ErrFull, store.ErrPaused}

// deliverImport hands an imported message to its recipients connected now.
func (h *Hub) deliverImport(clients map[string]*Client, result store.ImportResult) {
	for _, r := range result.Recipients {
		if c := sessionOwner(clients, r); c != nil {
			h.enqueue(clients, c, result.Msg)
		}
	}
}

// remoteSessions are the recipients connected at a version that shows a
// remote origin; the others get it when such a session claims their inbox.
func remoteSessions(clients map[string]*Client, agents []string) map[string]string {
	sessions := map[string]string{}
	for _, agent := range agents {
		if c := clients[agent]; c != nil && c.ProtocolVersion >= protocol.RemoteVersion {
			sessions[agent] = c.SessionID
		}
	}
	return sessions
}

// remoteAcknowledged sends an agent's acknowledgment of an imported
// message, queued with it, to its contact. Any acknowledgment may free
// inbox room a staged message waits for.
func (h *Hub) remoteAcknowledged(msg protocol.Message, report store.AckReport) {
	if msg.Stage != protocol.StageAgentAcknowledged {
		return
	}
	h.importsDue()
	if report.RemoteContact != "" {
		h.SyncMessages(report.RemoteContact)
	}
}

// routeRemote routes an agent's message to a paired workspace, and reports
// whether it was one: a message naming a workspace, or a reply to one
// imported from a workspace. It is kept in history and queued in one
// transaction, and leaves at once over a live link.
func (h *Hub) routeRemote(ctx context.Context, clients map[string]*Client, in Inbound) bool {
	route, remote, rerr := h.remoteRoute(ctx, in)
	if !remote {
		return false
	}
	if rerr == nil {
		rerr = h.sendRemote(ctx, clients, in, route)
	}
	if rerr != nil {
		h.enqueue(clients, in.From, rerr.frame(in.Msg.MessageID))
	}
	return true
}

// remoteRoute is how an agent's message reaches a paired workspace: the
// contact, and the store call that queues it.
type remoteRoute struct {
	contact string
	queue   func(ctx context.Context, out protocol.Message, text string, at time.Time) (store.RemoteReplyResult, error)
}

// remoteRoute is the route of a message for a paired workspace; remote is
// false for any other message.
func (h *Hub) remoteRoute(ctx context.Context, in Inbound) (remoteRoute, bool, *routingError) {
	if in.Msg.Workspace != "" {
		route, rerr := h.workspaceRoute(ctx, in)
		return route, true, rerr
	}
	if in.Msg.InReplyTo == "" {
		return remoteRoute{}, false, nil
	}
	parent, found, err := h.store.RemoteParent(ctx, in.Msg.InReplyTo)
	if err != nil || !found {
		return remoteRoute{}, false, nil
	}
	return h.replyRoute(in, parent), true, nil
}

// replyRoute answers an imported message, back to the workspace it came from.
func (h *Hub) replyRoute(in Inbound, parent store.RemoteImport) remoteRoute {
	return remoteRoute{parent.Contact, func(ctx context.Context, out protocol.Message, text string, at time.Time) (store.RemoteReplyResult, error) {
		return h.store.QueueRemoteReply(ctx, store.RemoteReply{Msg: out, SenderSession: in.From.SessionID, Parent: parent.LocalID, Text: text, Now: at})
	}}
}

// workspaceRoute is a message naming its workspace: new, or answering a
// message imported from that same workspace, never another.
func (h *Hub) workspaceRoute(ctx context.Context, in Inbound) (remoteRoute, *routingError) {
	contact, err := h.store.ResolveWorkspace(ctx, in.Msg.Workspace)
	if err != nil {
		return remoteRoute{}, workspaceError(err)
	}
	if in.Msg.InReplyTo == "" {
		return remoteRoute{contact, func(ctx context.Context, out protocol.Message, text string, at time.Time) (store.RemoteReplyResult, error) {
			return h.store.QueueWorkspaceSend(ctx, store.WorkspaceSend{Msg: out, SenderSession: in.From.SessionID, Contact: contact, Text: text,
				Review: wireAsk(in.Msg.ReviewAsk), Now: at})
		}}, nil
	}
	parent, found, err := h.store.RemoteParent(ctx, in.Msg.InReplyTo)
	if err != nil || !found {
		return remoteRoute{}, errOtherWorkspace
	}
	if parent.Contact != contact {
		return remoteRoute{}, errOtherWorkspace
	}
	return h.replyRoute(in, parent), nil
}

var errOtherWorkspace = &routingError{protocol.ErrMalformedFrame, "with workspace, in_reply_to answers a message from that workspace"}

// workspaceError is what an agent hears of a workspace it can't name.
func workspaceError(err error) *routingError {
	if errors.Is(err, store.ErrNoWorkspace) || errors.Is(err, store.ErrAmbiguousWorkspace) {
		return &routingError{protocol.ErrRecipientUnavailable, err.Error()}
	}
	return &routingError{protocol.ErrInternal, "paired workspaces unavailable"}
}

// sendRemote queues the message by its route, then confirms it and wakes
// the link.
func (h *Hub) sendRemote(ctx context.Context, clients map[string]*Client, in Inbound, route remoteRoute) *routingError {
	text, ok := remoteText(in.Msg)
	if !ok {
		return &routingError{protocol.ErrMalformedFrame, "a message to a paired workspace carries a text payload"}
	}
	ts := time.Now().UTC()
	out := in.Msg
	out.Seq, out.TS, out.From, out.SessionID, out.Origin = h.seq+1, &ts, in.From.ID, in.From.SessionID, protocol.OriginAgent
	result, err := route.queue(ctx, out, text, ts)
	if err != nil {
		return remoteReplyError(err)
	}
	if result.Repeated {
		out.Seq, out.Repeated = result.Seq, true
	} else {
		h.seq++
	}
	if out.AckRequested {
		h.enqueue(clients, in.From, protocol.Message{Type: protocol.TypeAck, MessageID: out.MessageID, Stage: protocol.StageAccepted,
			Seq: out.Seq, Durable: out.Durable, Repeated: out.Repeated})
	}
	h.SyncMessages(route.contact)
	h.MessagesChanged(route.contact)
	return nil
}

// payloadText is a {"text": …} payload's text.
func payloadText(payload json.RawMessage) (string, bool) {
	var p struct {
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || p.Text == nil {
		return "", false
	}
	return *p.Text, true
}

// remoteReplyErrors are the refusals of a reply the agent can act on.
var remoteReplyErrors = []struct {
	err    error
	code   string
	detail string
}{
	{messaging.ErrNotRecipient, protocol.ErrMalformedFrame, "a reply to a paired workspace's message comes from one of its recipients and goes to its sender"},
	{store.ErrNoImport, protocol.ErrMalformedFrame, "in_reply_to names no message from a paired workspace"},
	{messaging.ErrNotShared, protocol.ErrRecipientUnavailable, "this agent is not shared with that workspace"},
	{messaging.ErrUnknownContact, protocol.ErrRecipientUnavailable, "that workspace is no longer a confirmed contact"},
	{messaging.ErrOutboxFull, protocol.ErrInboxFull, "too many messages to that workspace are waiting to leave"},
	{messaging.ErrInvalid, protocol.ErrMalformedFrame, "a message to a paired workspace has a printable ID up to 128 bytes and 1 to 16 KiB of text"},
	{messaging.ErrConflict, protocol.ErrDuplicateMessage, "message ID already used for another message to that workspace"},
	{store.ErrDuplicateMessage, protocol.ErrDuplicateMessage, store.ErrDuplicateMessage.Error()},
}

func remoteReplyError(err error) *routingError {
	for _, e := range remoteReplyErrors {
		if errors.Is(err, e.err) {
			return &routingError{e.code, e.detail}
		}
	}
	return &routingError{protocol.ErrInternal, "reply not queued"}
}

// remoteText is what a message to a paired workspace says: its text, or
// for a review request without one, what it asks.
func remoteText(msg protocol.Message) (string, bool) {
	text, ok := payloadText(msg.Payload)
	if ok || msg.ReviewAsk == nil {
		return text, ok
	}
	a := msg.ReviewAsk
	return "Review request: " + a.Repository + "#" + a.Change, true
}

// wireAsk is a review request as it travels, or nil for a message.
func wireAsk(a *protocol.ReviewAsk) *messaging.ReviewAsk {
	if a == nil {
		return nil
	}
	return &messaging.ReviewAsk{Forge: a.Forge, Repository: a.Repository, Change: a.Change, Scope: a.Scope,
		Base: a.Base, Head: a.Head, Publish: a.Publish, Formal: a.Formal}
}
