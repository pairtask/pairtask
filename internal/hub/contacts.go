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
	protocol.ContactOpName:         (*Hub).contactName,
	// Pausing a contact (#276).
	protocol.ContactOpPause:  (*Hub).contactPause,
	protocol.ContactOpResume: (*Hub).contactPause,
	// Unpairing a contact (#276).
	protocol.ContactOpUnpair: (*Hub).contactUnpair,
	// The operator's acknowledgment (#287).
	protocol.ContactOpAck: (*Hub).contactAck,
	// Pending consent (#293).
	protocol.ContactOpConsentDecide: (*Hub).contactConsentDecide,
	protocol.ContactOpConsentOpt:    (*Hub).contactConsentOpt,
	protocol.ContactOpConsentReset:  (*Hub).contactConsentReset,
}

// operatorContact serves operator_contact.
func (h *Hub) operatorContact(ctx context.Context, clients map[string]*Client, req operatorRequest) protocol.Message {
	msg := req.msg
	req.clients = clients
	if rerr := contactFrameError(msg); rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	if consentOps[msg.ContactOp] {
		return h.operatorConsent(ctx, req)
	}
	if mobilityOps[msg.ContactOp] {
		return h.operatorMobility(ctx, req)
	}
	if rerr := h.contactRefused(ctx, clients, msg); rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	return h.contactOperation(ctx, req)
}

// contactFrameError refuses a malformed contact request, one that names a
// conversation, or one naming a pairing its client can't (#276).
func contactFrameError(msg protocol.Message) *routingError {
	if err := protocol.ValidateContactRequest(msg); err != nil {
		return &routingError{protocol.ErrMalformedFrame, err.Error()}
	}
	if rerr := contactEpochRefused(msg); rerr != nil {
		return rerr
	}
	return checkNoConversation(msg)
}

// contactRefused refuses a request whose home recipients, contact or ack
// bound don't hold.
func (h *Hub) contactRefused(ctx context.Context, clients map[string]*Client, msg protocol.Message) *routingError {
	if _, err := h.homeRecipients(ctx, clients, msg.ContactHere); err != nil {
		return &routingError{protocol.ErrMalformedFrame, err.Error()}
	}
	if rerr := h.readableContact(ctx, msg); rerr != nil {
		return rerr
	}
	return h.ackBound(ctx, msg)
}

// contactOperation runs a checked contact operation.
func (h *Hub) contactOperation(ctx context.Context, req operatorRequest) protocol.Message {
	switch req.msg.ContactOp {
	case protocol.ContactOpList:
		return h.contactList(ctx, req.msg)
	case protocol.ContactOpAddress:
		return h.contactAddress(ctx, req)
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

// opRefusals say why an operation of their kind was refused, if they know.
var opRefusals = []func(error) (*routingError, bool){ownRefusal, reviewRefusal, nameRefusal, pauseRefusal, ackRefusal, homeRefusal}

// ownRefusal is the operation's own refusal, as it said it.
func ownRefusal(err error) (*routingError, bool) {
	return errors.AsType[*routingError](err)
}

// contactRefusal is what the operator hears of a failed change; store
// details stay in the log.
func contactRefusal(err error) *routingError {
	for _, refusal := range opRefusals {
		if rerr, ok := refusal(err); ok {
			return rerr
		}
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
	home, err := h.homeRecipients(ctx, req.clients, msg.ContactHere)
	if err != nil {
		return protocol.Message{}, err
	}
	send := h.contactSendRow(msg, auditID, home)
	sent, err := h.store.OperatorContactSent(ctx, send)
	if err != nil {
		return protocol.Message{}, err
	}
	h.SyncMessages(msg.ContactKey)
	if !sent.Replayed { // the same send again changed nothing: delivered and announced the first time
		h.sentHere(ctx, req.clients, homeSend{msg: msg, seq: send.Seq, at: send.Now, recipients: home})
		h.MessagesChanged(msg.ContactKey)
	}
	view := versionedView(contactMessageView(sent.Row), msg.ProtocolVersion)
	if err := h.homeView(ctx, &view, sent.Row); err != nil { // sent all the same: the history reads it again
		h.log.Error("paired routing unavailable", "error", err)
	}
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp, ContactMessage: &view}, nil
}

// contactRead marks the shown messages read with its accepted audit, then
// announces the change and answers with the newest page.
func (h *Hub) contactRead(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	marked, err := h.store.OperatorContactRead(ctx, store.ContactRead{OperationID: msg.MessageID, Contact: msg.ContactKey,
		IDs: msg.ContactRead, AuditID: auditID, Now: h.contactNow(), Epoch: msg.ContactEpoch})
	if err != nil {
		return protocol.Message{}, err
	}
	if marked > 0 {
		h.SyncMessages(msg.ContactKey) // the receipts travel at the next session
		h.MessagesChanged(msg.ContactKey)
	}
	out := h.contactList(ctx, protocol.Message{MessageID: msg.MessageID, RequestID: msg.RequestID, ProtocolVersion: msg.ProtocolVersion,
		ContactKey: msg.ContactKey, ContactOp: msg.ContactOp, ContactMarked: &marked})
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

// contactList pages a contact's messages; it marks nothing read. A client
// at PairingHistoryVersion reads every pairing's, each tagged; an older one
// the newest pairing's alone (#276).
func (h *Hub) contactList(ctx context.Context, msg protocol.Message) protocol.Message {
	history := historyCapable(msg.ProtocolVersion)
	page := h.store.PeerMessages().CurrentPage
	if history {
		page = h.store.PeerMessages().Page
	}
	rows, more, err := page(ctx, msg.ContactKey, msg.ContactBefore, contactPage)
	if err != nil {
		return (&routingError{protocol.ErrInternal, "contact messages unavailable"}).frame(msg.MessageID)
	}
	return h.contactListPage(ctx, msg, contactRows{rows, more, history})
}

// contactRows are a page's messages as read, whether more follow, and
// whether its client reads them tagged with their pairing.
type contactRows struct {
	rows    []messaging.Row
	more    bool
	history bool
}

// contactListPage answers a list with the page its rows make.
func (h *Hub) contactListPage(ctx context.Context, msg protocol.Message, c contactRows) protocol.Message {
	page := protocol.ContactMessagePage{Contact: msg.ContactKey, Messages: []protocol.ContactMessageView{}, More: c.more,
		Sync: h.contactSyncView(msg.ContactKey)}
	if err := h.routingViews(ctx, &page, c); err != nil {
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
	view := protocol.ContactMessageView{ID: r.ID, Contact: r.Contact, Direction: r.Direction, Seq: r.N, CreatedAt: r.CreatedAt,
		Text: r.Text, State: r.State, RefusalClass: r.Refusal.Class, RefusalReason: r.Refusal.Reason,
		DeliveredAt: r.DeliveredAt, ReadAt: r.ReadAt, Receipt: r.Receipt, Cursor: r.Cursor,
		From: r.From, To: r.To, ReplyID: r.ReplyTo.ID, ReplySide: replySides[r.ReplyTo.Author], AckState: r.AckState, AckedAt: r.AckedAt,
		PairingEpoch: r.PairingEpoch}
	if r.Direction == messaging.DirKept { // a reply that never went there (#278): this side's, with no sequence there
		view.Direction, view.Seq = messaging.DirOut, 0
	}
	return view
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
	return h.contactList(ctx, protocol.Message{MessageID: msg.MessageID, RequestID: msg.RequestID, ProtocolVersion: msg.ProtocolVersion,
		ContactKey: msg.ContactKey, ContactOp: msg.ContactOp}), nil
}

// sharedSync fills what is shared with the contact and whether it has
// answered exactly that, from one snapshot.
func sharedSync(ctx context.Context, peer *store.PeerMessages, page *protocol.ContactMessagePage) error {
	snap, err := peer.SharedSnapshot(ctx, page.Contact)
	if err != nil {
		return err
	}
	page.Shared, page.SharedDigest, page.SharedAckDigest, page.SharedAckAt = snap.Agents, snap.Digest, snap.AckDigest, snap.AckAt
	page.SharedSync = protocol.SharedSyncPending
	if snap.Confirmed() {
		page.SharedSync = protocol.SharedSyncConfirmed
	}
	return nil
}

// routingViews fills a page's messages, with the agents that acknowledged
// each one sent to the contact's agents, and what each side shares.
func (h *Hub) routingViews(ctx context.Context, page *protocol.ContactMessagePage, c contactRows) error {
	peer := h.store.PeerMessages()
	for _, r := range c.rows {
		view, err := h.messageView(ctx, r)
		if err != nil {
			return err
		}
		if !c.history {
			untaggedMessage(&view)
		}
		page.Messages = append(page.Messages, view)
	}
	var err error
	if err = sharedSync(ctx, peer, page); err != nil {
		return err
	}
	if page.ReviewGrants, err = h.grantViews(ctx, page.Contact); err != nil {
		return err
	}
	if page.Directory, err = peer.PeerDirectory(ctx, page.Contact); err != nil {
		return err
	}
	page.Acks, err = h.ackSummary(ctx, page.Contact)
	return err
}

// messageView is a message as the operator reads it, with what else this
// side knows of it in the pairing it came under (#276), tagged with it.
func (h *Hub) messageView(ctx context.Context, r messaging.Row) (protocol.ContactMessageView, error) {
	view := contactMessageView(r)
	for _, add := range []func(context.Context, *protocol.ContactMessageView, messaging.Row) error{h.agentAcks, h.reviewView, h.consentView, h.homeView} {
		if err := add(ctx, &view, r); err != nil {
			return view, err
		}
	}
	return view, nil
}

// agentAcks adds to a message sent to the contact's agents those that
// acknowledged it.
func (h *Hub) agentAcks(ctx context.Context, view *protocol.ContactMessageView, r messaging.Row) error {
	if r.Direction != messaging.DirOut || len(r.To) == 0 {
		return nil
	}
	acks, err := h.store.PeerMessages().AgentAcksIn(ctx, r.Contact, r.PairingEpoch, r.ID)
	view.AgentAcks = acks
	return err
}
