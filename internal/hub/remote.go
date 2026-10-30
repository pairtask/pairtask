package hub

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
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

// importsDue asks the loop for an import pass. Safe from any goroutine; it
// never blocks.
func (h *Hub) importsDue() {
	select {
	case h.imports <- struct{}{}:
	default: // one is due already
	}
}

// drainImports imports what is staged, oldest first. One a full inbox
// keeps pending waits for the next pass: after an acknowledgment, a round,
// or a grant.
func (h *Hub) drainImports(ctx context.Context, clients map[string]*Client) {
	if h.store == nil {
		return // a hub without storage stages nothing
	}
	var after int64
	progressed := false
	for tried := 0; tried < importPass; tried += importBatch {
		pending, err := h.store.PendingImportsAfter(ctx, after, importBatch)
		if err != nil {
			h.log.Error("staged messages not read", "error", err)
			return
		}
		for _, imp := range pending {
			progressed = h.importOne(ctx, clients, imp) || progressed
			after = imp.Order
		}
		if len(pending) < importBatch {
			return
		}
	}
	if progressed {
		h.importsDue() // more than one pass tries
	}
}

// importOne imports one staged message at the next sequence, which only an
// import uses.
func (h *Hub) importOne(ctx context.Context, clients map[string]*Client, imp store.RemoteImport) bool {
	imp.Sessions = remoteSessions(clients, imp.Recipients)
	result, err := h.store.ImportRemote(ctx, imp, h.seq+1, time.Now())
	if err != nil {
		if !errors.Is(err, store.ErrInboxFull) && !errors.Is(err, store.ErrLocalIDsTaken) {
			h.log.Error("staged message not imported", "error", err)
		}
		return false
	}
	if !result.Changed || result.State != store.ImportImported {
		return result.Changed
	}
	h.seq++
	for _, r := range result.Recipients {
		if c := sessionOwner(clients, r); c != nil {
			h.enqueue(clients, c, result.Msg)
		}
	}
	return true
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

// routeRemoteReply routes an agent's reply to an imported message back to
// its contact, and reports whether it was one. The reply is kept in
// history and queued in one transaction; it leaves at once over a live
// link.
func (h *Hub) routeRemoteReply(ctx context.Context, clients map[string]*Client, in Inbound) bool {
	if in.Msg.InReplyTo == "" {
		return false
	}
	parent, found, err := h.store.RemoteParent(ctx, in.Msg.InReplyTo)
	if err != nil || !found {
		return false
	}
	out, rerr := h.queueRemoteReply(ctx, in, parent)
	if rerr != nil {
		h.enqueue(clients, in.From, rerr.frame(in.Msg.MessageID))
		return true
	}
	if out.AckRequested {
		h.enqueue(clients, in.From, protocol.Message{Type: protocol.TypeAck, MessageID: out.MessageID, Stage: protocol.StageAccepted,
			Seq: out.Seq, Durable: out.Durable, Repeated: out.Repeated})
	}
	h.SyncMessages(parent.Contact)
	h.MessagesChanged(parent.Contact)
	return true
}

func (h *Hub) queueRemoteReply(ctx context.Context, in Inbound, parent store.RemoteImport) (protocol.Message, *routingError) {
	text, ok := payloadText(in.Msg.Payload)
	if !ok {
		return protocol.Message{}, &routingError{protocol.ErrMalformedFrame, "a reply to a paired workspace carries a text payload"}
	}
	ts := time.Now().UTC()
	out := in.Msg
	out.Seq, out.TS, out.From, out.SessionID, out.Origin = h.seq+1, &ts, in.From.ID, in.From.SessionID, protocol.OriginAgent
	result, err := h.store.QueueRemoteReply(ctx, store.RemoteReply{Msg: out, SenderSession: in.From.SessionID, Parent: parent.LocalID, Text: text, Now: ts})
	if err != nil {
		return out, remoteReplyError(err)
	}
	if result.Repeated {
		out.Seq, out.Repeated = result.Seq, true
		return out, nil
	}
	h.seq++
	return out, nil
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
