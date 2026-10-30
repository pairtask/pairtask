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

// Contact messages (#210) for the operator: send to a confirmed contact,
// page its messages, mark the shown ones read, and ask for a sync. A send
// or a read is audited before it changes anything, and its change commits
// with the audit's accepted result; the text is never audited.

// contactPage bounds one page of a contact's messages.
const contactPage = 64

// contactChanges are the operations that change something, each committed
// with its audit row.
var contactChanges = map[string]func(*Hub, context.Context, operatorRequest, int64) (protocol.Message, error){
	protocol.ContactOpSend:    (*Hub).contactSend,
	protocol.ContactOpRead:    (*Hub).contactRead,
	protocol.ContactOpSync:    (*Hub).contactSync,
	protocol.ContactOpShare:   (*Hub).contactShare,
	protocol.ContactOpUnshare: (*Hub).contactShare,
	// Review requests (#235).
	protocol.ContactOpReviewGrant:  (*Hub).contactReviewGrant,
	protocol.ContactOpReviewAsk:    (*Hub).contactReviewAsk,
	protocol.ContactOpReviewDecide: (*Hub).contactReviewDecide,
}

// operatorContact serves operator_contact.
func (h *Hub) operatorContact(ctx context.Context, _ map[string]*Client, req operatorRequest) protocol.Message {
	msg := req.msg
	if err := protocol.ValidateContactRequest(msg); err != nil {
		return (&routingError{protocol.ErrMalformedFrame, err.Error()}).frame(msg.MessageID)
	}
	if rerr := h.confirmedContact(ctx, msg.ContactKey); rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	if msg.ContactOp == protocol.ContactOpList {
		return h.contactList(ctx, msg)
	}
	return h.contactChange(ctx, req)
}

// confirmedContact refuses anything but a confirmed contact, before any
// audit or change.
func (h *Hub) confirmedContact(ctx context.Context, contact string) *routingError {
	ok, err := h.store.PeerMessages().Confirmed(ctx, contact)
	switch {
	case err != nil:
		return &routingError{protocol.ErrInternal, "contacts unavailable"}
	case !ok:
		return &routingError{protocol.ErrContactRefused, "not a confirmed contact"}
	}
	return nil
}

// contactChange audits, applies and announces one changing operation.
func (h *Hub) contactChange(ctx context.Context, req operatorRequest) protocol.Message {
	msg := req.msg
	auditID, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "contact " + msg.ContactOp, PrincipalUID: req.uid,
		MessageID: msg.MessageID, Scope: msg.ContactKey, Result: store.AuditPending})
	if err != nil {
		return (&routingError{protocol.ErrInternal, "operator audit unavailable; contact messages unchanged"}).frame(msg.MessageID)
	}
	out, err := contactChanges[msg.ContactOp](h, ctx, req, auditID)
	if err != nil {
		rerr := contactRefusal(err)
		h.finishAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditRejected, Detail: rerr.code})
		return rerr.frame(msg.MessageID)
	}
	return out
}

// contactRefusal is what the operator hears of a failed change; store
// details stay in the log.
func contactRefusal(err error) *routingError {
	if rerr, ok := reviewRefusal(err); ok {
		return rerr
	}
	switch {
	case errors.Is(err, messaging.ErrConflict):
		return &routingError{protocol.ErrContactRefused, "message_id already names another operation"}
	case errors.Is(err, messaging.ErrOutboxFull):
		return &routingError{protocol.ErrContactRefused, "too many messages waiting for this contact"}
	case errors.Is(err, messaging.ErrUnknownContact):
		return &routingError{protocol.ErrContactRefused, "not a confirmed contact"}
	case errors.Is(err, store.ErrTooManyGrants):
		return &routingError{protocol.ErrContactRefused, "a contact is shared at most 64 agents"}
	case errors.Is(err, messaging.ErrInvalid):
		return &routingError{protocol.ErrMalformedFrame, "message not accepted"}
	}
	return &routingError{protocol.ErrInternal, "contact messages unavailable"}
}

// contactSend queues the message with its accepted audit, then asks for a
// session and announces the change.
func (h *Hub) contactSend(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	row, err := h.store.OperatorContactSend(ctx, store.ContactSend{OperationID: msg.MessageID, Contact: msg.ContactKey,
		Text: msg.ContactText, To: msg.ContactTo, AuditID: auditID, Now: h.contactNow()})
	if err != nil {
		return protocol.Message{}, err
	}
	h.SyncMessages(msg.ContactKey)
	h.MessagesChanged(msg.ContactKey)
	view := contactMessageView(row)
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp, ContactMessage: &view}, nil
}

// contactRead marks the shown messages read with its accepted audit, then
// announces the change and answers with the newest page.
func (h *Hub) contactRead(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	marked, err := h.store.OperatorContactRead(ctx, store.ContactRead{OperationID: msg.MessageID, Contact: msg.ContactKey,
		IDs: msg.ContactRead, AuditID: auditID, Now: h.contactNow()})
	if err != nil {
		return protocol.Message{}, err
	}
	if marked > 0 {
		h.SyncMessages(msg.ContactKey) // the receipts travel at the next session
		h.MessagesChanged(msg.ContactKey)
	}
	out := h.contactList(ctx, protocol.Message{MessageID: msg.MessageID, RequestID: msg.RequestID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp, ContactMarked: &marked})
	return out, nil
}

// contactSync finishes its audit, then asks for a session: the answer says
// one was requested, never that it ran.
func (h *Hub) contactSync(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	if err := h.store.FinishOperatorAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditAccepted}); err != nil {
		return protocol.Message{}, err
	}
	h.SyncMessages(msg.ContactKey)
	view := h.contactSyncView(msg.ContactKey)
	view.Requested = true
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp, ContactSync: &view}, nil
}

// contactList pages a contact's messages; it marks nothing read.
func (h *Hub) contactList(ctx context.Context, msg protocol.Message) protocol.Message {
	rows, more, err := h.store.PeerMessages().Page(ctx, msg.ContactKey, msg.ContactBefore, contactPage)
	if err != nil {
		return (&routingError{protocol.ErrInternal, "contact messages unavailable"}).frame(msg.MessageID)
	}
	page := protocol.ContactMessagePage{Contact: msg.ContactKey, Messages: []protocol.ContactMessageView{}, More: more,
		Sync: h.contactSyncView(msg.ContactKey)}
	if err := h.routingViews(ctx, &page, rows); err != nil {
		return (&routingError{protocol.ErrInternal, "contact messages unavailable"}).frame(msg.MessageID)
	}
	out := protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp, ContactPage: &page, ContactMarked: msg.ContactMarked}
	if err := fitContactPage(&out, msg.RequestID); err != nil {
		h.log.Error("contact page not encodable", "error", err)
		return (&routingError{protocol.ErrInternal, "contact messages unavailable"}).frame(msg.MessageID)
	}
	return out
}

// frameTail is the frame's own line end, and slack.
const frameTail = 16

// fitContactPage keeps the newest messages that fit one frame, as encoded
// with the request ID the session will put on the response: every message
// kept is whole, the newest always stays, and More tells the client older
// ones remain, from the last cursor returned. An encoding failure is an
// error, never an empty page.
func fitContactPage(out *protocol.Message, requestID string) error {
	page := out.ContactPage
	all := page.Messages
	page.Messages = []protocol.ContactMessageView{}
	envelope := *out
	envelope.RequestID = requestID
	base, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	used := len(base) + frameTail
	for i, m := range all {
		encoded, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if i > 0 && used+len(encoded)+1 > protocol.MaxFrameBytes {
			page.Messages, page.More = all[:i], true
			return nil
		}
		used += len(encoded) + 1
	}
	page.Messages = all
	return nil
}

func (h *Hub) contactNow() time.Time {
	if h.pair.cfg.Clock != nil {
		return h.pair.cfg.Clock().UTC()
	}
	return time.Now().UTC()
}

func contactMessageView(r messaging.Row) protocol.ContactMessageView {
	return protocol.ContactMessageView{ID: r.ID, Contact: r.Contact, Direction: r.Direction, Seq: r.N, CreatedAt: r.CreatedAt,
		Text: r.Text, State: r.State, RefusalClass: r.Refusal.Class, RefusalReason: r.Refusal.Reason,
		DeliveredAt: r.DeliveredAt, ReadAt: r.ReadAt, Receipt: r.Receipt, Cursor: r.Cursor,
		From: r.From, To: r.To, ReplyID: r.ReplyTo.ID, ReplySide: replySides[r.ReplyTo.Author]}
}

// replySides name a reply's author for the operator.
var replySides = map[byte]string{messaging.ReplyBySender: protocol.ReplyBySender, messaging.ReplyByReceiver: protocol.ReplyByReceiver}

// contactSyncView is the sync status as the operator sees it.
func (h *Hub) contactSyncView(contact string) protocol.ContactSyncView {
	s := h.MessageSyncStatus(contact)
	view := protocol.ContactSyncView{Contact: contact, Dials: s.Dials, Live: s.Live, LastFailure: s.LastFailure, Failures: s.Failures}
	view.LastAttempt, view.LastSuccess, view.NextAt = optionalTime(s.LastAttempt), optionalTime(s.LastSuccess), optionalTime(s.NextAt)
	return view
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// contactShare changes what the contact may message here, with its audit,
// then wakes the link, so the contact learns at once, and imports what the
// change settles; it answers with the newest page.
func (h *Hub) contactShare(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	change := h.store.PeerMessages().Share
	if msg.ContactOp == protocol.ContactOpUnshare {
		change = h.store.PeerMessages().Unshare
	}
	if _, err := change(ctx, msg.ContactKey, msg.ContactAgents, h.contactNow()); err != nil {
		return protocol.Message{}, err
	}
	if err := h.store.FinishOperatorAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditAccepted}); err != nil {
		return protocol.Message{}, err
	}
	h.SyncMessages(msg.ContactKey)
	h.importsDue()
	h.MessagesChanged(msg.ContactKey)
	return h.contactList(ctx, protocol.Message{MessageID: msg.MessageID, RequestID: msg.RequestID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp}), nil
}

// routingViews fills a page's messages, with the agents that acknowledged
// each one sent to the contact's agents, and what each side shares.
func (h *Hub) routingViews(ctx context.Context, page *protocol.ContactMessagePage, rows []messaging.Row) error {
	peer := h.store.PeerMessages()
	for _, r := range rows {
		view := contactMessageView(r)
		if err := h.agentAcks(ctx, &view, r); err != nil {
			return err
		}
		if err := h.reviewView(ctx, &view, r); err != nil {
			return err
		}
		page.Messages = append(page.Messages, view)
	}
	var err error
	if page.Shared, err = peer.SharedAgents(ctx, page.Contact); err != nil {
		return err
	}
	if page.ReviewGrants, err = h.grantViews(ctx, page.Contact); err != nil {
		return err
	}
	page.Directory, err = peer.PeerDirectory(ctx, page.Contact)
	return err
}

// agentAcks adds to a message sent to the contact's agents those that
// acknowledged it.
func (h *Hub) agentAcks(ctx context.Context, view *protocol.ContactMessageView, r messaging.Row) error {
	if r.Direction != messaging.DirOut || len(r.To) == 0 {
		return nil
	}
	acks, err := h.store.PeerMessages().AgentAcks(ctx, r.Contact, r.ID)
	view.AgentAcks = acks
	return err
}
