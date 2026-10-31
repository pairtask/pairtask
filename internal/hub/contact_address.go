package hub

import (
	"context"
	"errors"
	"sync"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// /pair address (contact_address) moves a contact to a new HOST:PORT once a
// probe there proved the same identity: a workspace that came back on
// another address is reached again without pairing anew. Only the side that
// dials the contact may move it. The probe runs off the Run goroutine and
// only handshakes, never exchanging a message; the one answer to the
// request comes after it, from the stored outcome.

// maxAddressChecks bounds the probes running at once, over all contacts.
const maxAddressChecks = 4

// addressRefusal is a fixed reason an address change was refused; none
// carries text from the peer.
type addressRefusal string

const (
	addressRunning     addressRefusal = "an address check for this workspace is already running"
	addressBusy        addressRefusal = "too many address checks are running; try again shortly"
	addressInterrupted addressRefusal = "interrupted; send a new request"
	addressNoDialing   addressRefusal = "this broker can't dial out to check an address"
)

// unreachable and mismatch name a candidate the probe couldn't use.
func unreachable(candidate string) addressRefusal {
	return addressRefusal(candidate + " is unreachable")
}

func mismatch(candidate string) addressRefusal {
	return addressRefusal("the workspace at " + candidate + " isn't them: identity mismatch")
}

// addressRequest is one address change: its operation (the message ID), the
// contact it moves and where to.
type addressRequest struct {
	id, contact, candidate string
}

func (r addressRequest) refused(why addressRefusal) protocol.Message {
	return (&routingError{protocol.ErrContactRefused, string(why)}).frame(r.id)
}

func (r addressRequest) failed(code, detail string) protocol.Message {
	return (&routingError{code, detail}).frame(r.id)
}

// addressChecks are the probes running now, by contact.
type addressChecks struct {
	mu      sync.Mutex
	running map[string]string // contact → operation ID
}

// claim reserves r's contact for it, or says why not.
func (a *addressChecks) claim(r addressRequest) addressRefusal {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.running[r.contact] != "":
		return addressRunning
	case len(a.running) >= maxAddressChecks:
		return addressBusy
	}
	if a.running == nil {
		a.running = map[string]string{}
	}
	a.running[r.contact] = r.id
	return ""
}

func (a *addressChecks) release(r addressRequest) {
	a.mu.Lock()
	delete(a.running, r.contact)
	a.mu.Unlock()
}

// runs: r is running in this broker now.
func (a *addressChecks) runs(r addressRequest) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running[r.contact] == r.id
}

// contactAddress begins an address change, or answers a known one. A new
// one is answered later, by the probe's worker, so the Run goroutine never
// waits on the network.
func (h *Hub) contactAddress(ctx context.Context, req operatorRequest) protocol.Message {
	r := addressRequest{id: req.msg.MessageID, contact: req.msg.ContactKey, candidate: req.msg.ContactText}
	c, answer, ok := h.addressContact(ctx, r)
	if !ok {
		return answer
	}
	outcome, known, err := h.store.BeginContactAddress(ctx, store.AddressOp{ID: r.id, Contact: r.contact, Candidate: r.candidate,
		At: h.contactNow(), PrincipalUID: req.uid})
	if err != nil || known {
		return h.addressKnown(ctx, r, outcome, err)
	}
	if why := h.addressAdmission(c, r); why != "" {
		return h.refuseAddress(ctx, r, why)
	}
	h.startAddressCheck(req, addressCheck{request: r, contact: c, own: h.identity.Fingerprint()})
	return answerLater
}

// addressContact is the contact to move, once this broker can dial at all.
func (h *Hub) addressContact(ctx context.Context, r addressRequest) (peer.Contact, protocol.Message, bool) {
	if problem := h.loadIdentity(ctx); problem != "" || !h.canDial() {
		return peer.Contact{}, r.refused(addressNoDialing), false
	}
	c, found, err := h.store.Contact(ctx, r.contact)
	if err != nil || !found {
		return peer.Contact{}, r.failed(protocol.ErrInternal, "contacts unavailable"), false
	}
	return c, protocol.Message{}, true
}

// addressKnown answers a begin that found the operation already stored, or
// failed.
func (h *Hub) addressKnown(ctx context.Context, r addressRequest, outcome store.AddressOutcome, err error) protocol.Message {
	switch {
	case errors.Is(err, store.ErrAddressOpConflict):
		return r.failed(protocol.ErrMalformedFrame, err.Error())
	case err != nil:
		return r.failed(protocol.ErrInternal, "operator audit unavailable; address unchanged")
	}
	return h.addressReplay(ctx, r, outcome)
}

// addressAdmission is why a new check can't start now, if it can't.
func (h *Hub) addressAdmission(c peer.Contact, r addressRequest) addressRefusal {
	if !dials(c, h.identity.Fingerprint()) {
		return addressRefusal(peer.ErrNotDialing.Error())
	}
	return h.addresses.claim(r)
}

// addressReplay answers a known operation from what is stored: an accepted
// one with the current snapshot, never applying its candidate again.
func (h *Hub) addressReplay(ctx context.Context, r addressRequest, outcome store.AddressOutcome) protocol.Message {
	switch {
	case outcome.State == store.AddressAccepted:
		return h.addressAccepted(ctx, r)
	case outcome.State == store.AddressRefused:
		return r.refused(addressRefusal(outcome.Reason))
	case h.addresses.runs(r):
		return r.refused(addressRunning)
	}
	// Running in the store but not here: the broker stopped mid-probe.
	return h.refuseAddress(ctx, r, addressInterrupted)
}

// refuseAddress settles a running operation as refused and answers so.
func (h *Hub) refuseAddress(ctx context.Context, r addressRequest, why addressRefusal) protocol.Message {
	if err := h.store.RefuseContactAddress(ctx, r.id, string(why)); err != nil {
		return r.failed(protocol.ErrInternal, "operator audit unavailable")
	}
	return r.refused(why)
}

// addressAccepted is the answer to an accepted change: the snapshot as it
// is now, whatever the request asked for.
func (h *Hub) addressAccepted(ctx context.Context, r addressRequest) protocol.Message {
	snapshot, rerr := h.applyPairing(ctx, pairingOps[protocol.PairingOpList], "")
	if rerr != nil {
		return rerr.frame(r.id)
	}
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: r.id, ContactKey: r.contact,
		ContactOp: protocol.ContactOpAddress, PairingResult: snapshot}
}

// addressCheck is one probe: the request, the contact as read when it
// began, and this workspace's fingerprint for dial ownership.
type addressCheck struct {
	request addressRequest
	contact peer.Contact
	own     string
}

// startAddressCheck probes the candidate off the Run goroutine, bounded by
// AttemptBudget and cancelled at shutdown, commits a proven address, and
// answers through the Run goroutine, which re-reads the contact.
func (h *Hub) startAddressCheck(req operatorRequest, check addressCheck) {
	probe := addressProbe{cfg: h.pair.cfg, identity: h.identity, pin: check.contact.IdentityKey, candidate: check.request.candidate}
	runCtx := h.pair.runCtx
	h.pair.workers.Add(1)
	go func() {
		defer h.pair.workers.Done()
		defer h.addresses.release(check.request)
		netCtx, cancelNet := context.WithTimeout(runCtx, AttemptBudget)
		defer cancelNet()
		storeCtx, cancelStore := context.WithTimeout(context.WithoutCancel(runCtx), AttemptBudget+PairingGrace)
		defer cancelStore()
		finish := h.settleAddress(storeCtx, check, probe.run(netCtx))
		h.finishOperator(runCtx, req, finish)
	}()
}

// addressProbe is what a probe needs: how to dial, who this workspace is,
// the identity the candidate must present, and where.
type addressProbe struct {
	cfg       PairingConfig
	identity  *peer.WorkspaceIdentity
	pin       string
	candidate string
}

// run dials the candidate and completes a handshake pinned to the contact's
// identity, then closes: nothing is exchanged. It returns why the address
// can't be used, or "".
func (p addressProbe) run(ctx context.Context) addressRefusal {
	raw, err := p.cfg.Dial(ctx, p.candidate)
	if err != nil {
		return unreachable(p.candidate)
	}
	conn, err := firstcontact.MessageClient(ctx, raw, p.identity, p.cfg.Clock, p.pin)
	switch {
	case errors.Is(err, peer.ErrPeerMismatch):
		return mismatch(p.candidate)
	case err != nil:
		return unreachable(p.candidate)
	}
	conn.Close()
	return ""
}

// settleAddress stores the probe's outcome and returns the step that
// answers it on the Run goroutine.
func (h *Hub) settleAddress(ctx context.Context, check addressCheck, why addressRefusal) func(context.Context) protocol.Message {
	r := check.request
	if why != "" {
		return answered(h.refuseAddress(ctx, r, why))
	}
	if answer, ok := h.commitAddress(ctx, check); !ok {
		return answered(answer)
	}
	return func(ctx context.Context) protocol.Message { return h.addressMoved(ctx, r) }
}

// commitAddress stores a proven address; a refusal or failure is the
// answer instead.
func (h *Hub) commitAddress(ctx context.Context, check addressCheck) (protocol.Message, bool) {
	c, r := check.contact, check.request
	_, err := h.store.ChangeContactAddress(ctx, store.ContactAddress{OpID: r.id, Contact: r.contact, From: c.Destination, FromAt: c.DestinationAt,
		To: r.candidate, At: h.contactNow(), StillDials: func(now peer.Contact) bool { return dials(now, check.own) }})
	switch {
	case errors.Is(err, peer.ErrDestinationChanged), errors.Is(err, peer.ErrNotDialing):
		return r.refused(addressRefusal(err.Error())), false
	case err != nil:
		return r.failed(protocol.ErrInternal, "address not stored"), false
	}
	return protocol.Message{}, true
}

// answered is a finish step whose answer is already known.
func answered(m protocol.Message) func(context.Context) protocol.Message {
	return func(context.Context) protocol.Message { return m }
}

// addressMoved, on the Run goroutine, adopts the contact's destination as
// stored now (a newer redemption may have moved it again), wakes its
// sync, and answers with the snapshot.
func (h *Hub) addressMoved(ctx context.Context, r addressRequest) protocol.Message {
	h.readDestination(r.contact)
	h.PairingChanged()
	h.SyncMessages(r.contact)
	return h.addressAccepted(ctx, r)
}
