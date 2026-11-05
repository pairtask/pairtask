package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Pending consent (#293). The operator lists held requests and decides
// them through operator_contact; the hub derives the evidence a decision
// commits, never the client. Session evidence holds only while the
// connection that decided owns this broker incarnation: the loop checks it
// at each protected boundary, dispatch and the publication reservation,
// so a disconnect, a replacement or a restart stops what hasn't crossed.

// consentOps are the consent operations of operator_contact.
var consentOps = map[string]bool{
	protocol.ContactOpConsentList: true, protocol.ContactOpConsentDecide: true,
	protocol.ContactOpConsentOpt: true, protocol.ContactOpConsentReset: true,
}

// liveOwner is the operator connection that owns the broker now.
func (h *Hub) liveOwner() store.LiveOwner {
	if h.owner == nil {
		return store.LiveOwner{}
	}
	return store.LiveOwner{Epoch: h.epoch, Gen: h.owner.gen}
}

// consentFenced: the import was allowed for a session that no longer owns
// the broker. Once evidence, or none, is never fenced here.
func (h *Hub) consentFenced(imp store.RemoteImport) bool {
	if imp.Consent.Kind != protocol.ConsentSession {
		return false
	}
	return !h.liveOwner().Holds(imp.Consent)
}

// withholdConsent settles a fenced import for good: nothing is delivered,
// and the requester hears its session ended.
func (h *Hub) withholdConsent(ctx context.Context, imp store.RemoteImport) {
	result, err := h.store.WithholdConsent(ctx, imp, protocol.ConsentSessionEnded, h.contactNow())
	if err != nil {
		h.log.Error("ended session's import not withheld", "error", err)
		return
	}
	if result.Changed {
		h.SyncMessages(imp.Contact) // its outcome leaves at once
	}
}

// consentView adds to a message this side sent where the contact's hold of
// it stands, when it held it for its operator: one line on the message's
// own row, whatever else the request became.
func (h *Hub) consentView(ctx context.Context, view *protocol.ContactMessageView, r messaging.Row) error {
	if r.Direction != messaging.DirOut {
		return nil
	}
	held, found, err := h.store.PeerMessages().HeldStatus(ctx, r.Contact, r.ID)
	if found {
		view.Consent, view.ConsentReason = held.State, held.Reason
	}
	return err
}

// operatorConsent serves the consent operations: the list reads only;
// the others are audited changes.
func (h *Hub) operatorConsent(ctx context.Context, req operatorRequest) protocol.Message {
	if req.msg.ContactOp == protocol.ContactOpConsentList {
		return h.consentList(ctx, req.msg)
	}
	if rerr := h.confirmedContact(ctx, req.msg.ContactKey); rerr != nil {
		return rerr.frame(req.msg.MessageID)
	}
	return h.contactChange(ctx, req)
}

// consentList pages held requests, of one contact or all, newest first.
// It marks, receipts, charges, extends and audits nothing.
func (h *Hub) consentList(ctx context.Context, msg protocol.Message) protocol.Message {
	page, err := h.store.ConsentPage(ctx, msg.ContactKey, msg.ContactConsentBefore, consentListed)
	if err != nil {
		h.log.Error("held requests not read", "error", err)
		return (&routingError{protocol.ErrInternal, "held requests unavailable"}).frame(msg.MessageID)
	}
	out := consentAnswer(msg)
	out.ConsentPage = &protocol.ConsentPage{Items: page.Items, Total: page.Total, More: page.More, Next: page.Next}
	if out.ConsentPage.Items == nil {
		out.ConsentPage.Items = []protocol.ConsentView{}
	}
	if err := fitConsentPage(&out, msg.RequestID); err != nil {
		h.log.Error("held requests not encodable", "error", err)
		return (&routingError{protocol.ErrInternal, "held requests unavailable"}).frame(msg.MessageID)
	}
	return out
}

// consentListed bounds one page of held requests; the frame may hold fewer.
const consentListed = 64

// fitConsentPage keeps the newest held requests that fit one frame, each
// whole; More and Next go on from the last one kept.
func fitConsentPage(out *protocol.Message, requestID string) error {
	page := out.ConsentPage
	all := page.Items
	envelope := *out
	envelope.RequestID = requestID
	envelope.ConsentPage = &protocol.ConsentPage{Items: []protocol.ConsentView{}, Total: page.Total, More: true, Next: 1 << 62}
	base, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	used := len(base) + frameTail
	for i, v := range all {
		encoded, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if i > 0 && used+len(encoded)+1 > protocol.MaxFrameBytes {
			page.Items, page.More, page.Next = all[:i], true, all[i-1].Cursor
			return nil
		}
		used += len(encoded) + 1
	}
	return nil
}

// consentAnswer is a consent operation's result frame.
func consentAnswer(msg protocol.Message) protocol.Message {
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactKey: msg.ContactKey,
		ContactOp: msg.ContactOp}
}

// contactConsentDecide commits the operator's decision on one held request
// as it was shown, under the evidence the hub derives. A stale view or an
// ended session decides nothing and says why.
func (h *Hub) contactConsentDecide(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	d := req.msg.ConsentDecide
	if reason := h.decisionStale(req); reason != "" {
		return h.consentRefused(ctx, req.msg, auditID, protocol.ConsentResult{ConsentID: d.ConsentID, State: consentUnchanged, Reason: reason}), nil
	}
	out, err := h.store.DecideConsent(ctx, store.ConsentDecision{ID: d.ConsentID, Digest: d.Digest, Revision: d.Revision,
		Choice: d.Choice, Evidence: h.evidence(d.Choice, req.sess), AuditID: auditID, Now: h.contactNow()})
	if reason, ok := undecided(err); ok {
		h.noteConsent(ctx) // an expiry it found is news
		return h.consentRefused(ctx, req.msg, auditID, protocol.ConsentResult{ConsentID: d.ConsentID, State: orUnchanged(out.State), Reason: reason}), nil
	}
	if err != nil {
		return protocol.Message{}, err
	}
	h.consentDecided(ctx, req.msg.ContactKey, out)
	answer := consentAnswer(req.msg)
	answer.ConsentResult = &protocol.ConsentResult{ConsentID: out.ID, State: out.State, Outcome: d.Choice, Reason: out.Reason}
	return answer, nil
}

// consentUnchanged is a refused decision's state: whatever the request is,
// this decision didn't change it.
const consentUnchanged = "unchanged"

func orUnchanged(state string) string {
	if state == "" {
		return consentUnchanged
	}
	return state
}

// decisionStale is why a decision can't be taken as shown: another broker
// incarnation, a connection that no longer owns the broker, or another
// generation than the one deciding. The client's epoch and generation only
// guard a stale view; they never grant.
func (h *Hub) decisionStale(req operatorRequest) string {
	d := req.msg.ConsentDecide
	switch {
	case d.Epoch != h.epoch:
		return protocol.ConsentStale
	case req.sess == nil || h.owner != req.sess:
		return protocol.ConsentSessionEnded
	case d.Gen != req.sess.gen:
		return protocol.ConsentStale
	}
	return ""
}

// evidence is what a choice commits as its authority: once or this
// connection's session; the other choices need none.
func (h *Hub) evidence(choice string, sess *OperatorSession) store.ConsentEvidence {
	e := store.ConsentEvidence{Epoch: h.epoch, Gen: sess.gen}
	if choice == protocol.ConsentOnce || choice == protocol.ConsentSession {
		e.Kind = choice
	}
	return e
}

// undecidedReasons are the store's refusals of a decision that are
// answers, not failures.
var undecidedReasons = []struct {
	err    error
	reason string
}{
	{store.ErrConsentStale, protocol.ConsentStale},
	{store.ErrPaused, protocol.ConsentPaused},
	{store.ErrFenced, protocol.ConsentPaused},
	{messaging.ErrUnknownContact, protocol.ConsentRevoked},
}

// undecided is why the store refused a decision, when that is an answer.
func undecided(err error) (string, bool) {
	for _, u := range undecidedReasons {
		if errors.Is(err, u.err) {
			return u.reason, true
		}
	}
	return "", false
}

// consentRefused finishes a refused decision's audit and answers why.
func (h *Hub) consentRefused(ctx context.Context, msg protocol.Message, auditID int64, r protocol.ConsentResult) protocol.Message {
	h.finishAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditRejected, Detail: r.Reason})
	answer := consentAnswer(msg)
	answer.ConsentResult = &r
	return answer
}

// consentDecided announces a decision: the operator's views refresh, an
// allowed request goes to dispatch, a declined one's outcome leaves.
func (h *Hub) consentDecided(ctx context.Context, contact string, out store.ConsentOutcome) {
	h.noteConsent(ctx)
	if out.Dispatched {
		h.importsDue()
	}
	if out.State != protocol.ConsentLater {
		h.SyncMessages(contact)
		h.MessagesChanged(contact)
	}
}

// contactConsentOpt turns holding a contact's undecided requests on or off.
func (h *Hub) contactConsentOpt(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	if err := h.store.SetConsentOptIn(ctx, msg.ContactKey, *msg.ConsentOn, auditID, h.contactNow()); err != nil {
		return protocol.Message{}, err
	}
	h.MessagesChanged(msg.ContactKey)
	return consentAnswer(msg), nil
}

// contactConsentReset makes one agent's operation for a contact undecided
// again: an explicit local action, never implied by opting in.
func (h *Hub) contactConsentReset(ctx context.Context, req operatorRequest, auditID int64) (protocol.Message, error) {
	msg := req.msg
	if err := h.store.ResetConsentScope(ctx, msg.ContactKey, msg.ConsentAgent, msg.ConsentOperation, auditID, h.contactNow()); err != nil {
		return protocol.Message{}, err
	}
	h.MessagesChanged(msg.ContactKey)
	return consentAnswer(msg), nil
}

// consentDue asks the loop to look at held requests, after something that
// may have held or expired one. Safe from any goroutine; it never blocks.
func (h *Hub) consentDue() {
	h.consentCheck.Store(true)
	select {
	case h.pair.changed <- struct{}{}:
	default: // one is due already
	}
}

// noteConsent tells the operator held requests moved: one arrived, was
// decided or expired. It runs on the Run goroutine.
func (h *Hub) noteConsent(ctx context.Context) {
	if h.store == nil {
		return
	}
	mark, err := h.store.ConsentMark(ctx)
	if err != nil {
		h.log.Warn("held requests unreadable", "err", err)
		return
	}
	if mark == h.consentMark {
		return
	}
	h.consentMark = mark
	h.emit(protocol.SessionEvent{Kind: protocol.EventConsentChanged})
}

// consentConflicts are outcomes a contact sent that contradicted the one
// this side holds; refused on the wire, reported here, never stored.
type consentConflicts struct {
	mu      sync.Mutex
	pending []protocol.SessionEvent
}

// ConsentConflicts reports a session's refused contradicting outcomes:
// logged now, announced to the operator on the Run goroutine. Safe from any
// goroutine.
func (h *Hub) ConsentConflicts(contact string, ids []string) {
	if len(ids) == 0 {
		return
	}
	c := &h.conflicts
	c.mu.Lock()
	for _, id := range ids {
		h.log.Warn("paired workspace contradicted a held request's outcome; refused", "contact", contact, "request", id)
		c.pending = append(c.pending, protocol.SessionEvent{Kind: protocol.EventConsentConflict, ContactKey: contact, MessageID: id})
	}
	c.mu.Unlock()
	select {
	case h.pair.changed <- struct{}{}:
	default: // one is due already
	}
}

// announceConflicts emits the reported conflicts; it runs on the Run
// goroutine.
func (h *Hub) announceConflicts() {
	c := &h.conflicts
	c.mu.Lock()
	events := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, ev := range events {
		h.emit(ev)
	}
}
