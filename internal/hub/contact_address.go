package hub

import (
	"context"
	"errors"
	"fmt"
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

// Fixed refusals; none carries text from the peer.
const (
	addressRunning     = "an address check for this workspace is already running"
	addressBusy        = "too many address checks are running; try again shortly"
	addressInterrupted = "interrupted; send a new request"
)

// addressChecks are the probes running now, by contact.
type addressChecks struct {
	mu      sync.Mutex
	running map[string]string // contact → operation ID
}

// claim reserves contact for operation id, or says why not.
func (a *addressChecks) claim(contact, id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.running[contact] != "":
		return addressRunning
	case len(a.running) >= maxAddressChecks:
		return addressBusy
	}
	if a.running == nil {
		a.running = map[string]string{}
	}
	a.running[contact] = id
	return ""
}

func (a *addressChecks) release(contact string) {
	a.mu.Lock()
	delete(a.running, contact)
	a.mu.Unlock()
}

// runs: operation id is running in this broker now.
func (a *addressChecks) runs(contact, id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running[contact] == id
}

// contactAddress begins an address change, or answers a known one. A new
// one is answered later, by the probe's worker, so the Run goroutine never
// waits on the network.
func (h *Hub) contactAddress(ctx context.Context, req operatorRequest) protocol.Message {
	msg := req.msg
	if problem := h.loadIdentity(ctx); problem != "" || !h.canDial() {
		return (&routingError{protocol.ErrContactRefused, "this broker can't dial out to check an address"}).frame(msg.MessageID)
	}
	c, found, err := h.store.Contact(ctx, msg.ContactKey)
	if err != nil || !found {
		return (&routingError{protocol.ErrInternal, "contacts unavailable"}).frame(msg.MessageID)
	}
	op := store.AddressOp{ID: msg.MessageID, Contact: c.IdentityKey, Candidate: msg.ContactText, At: h.contactNow(), PrincipalUID: req.uid}
	outcome, known, err := h.store.BeginContactAddress(ctx, op)
	switch {
	case errors.Is(err, store.ErrAddressOpConflict):
		return (&routingError{protocol.ErrMalformedFrame, err.Error()}).frame(msg.MessageID)
	case err != nil:
		return (&routingError{protocol.ErrInternal, "operator audit unavailable; address unchanged"}).frame(msg.MessageID)
	case known:
		return h.addressReplay(ctx, op, outcome)
	}
	if refusal := h.addressAdmission(c, op.ID); refusal != "" {
		return h.refuseAddress(ctx, op.ID, refusal)
	}
	h.startAddressCheck(req, addressCheck{contact: c, candidate: op.Candidate, own: h.identity.Fingerprint()})
	return answerLater
}

// addressAdmission is why a new check can't start now, if it can't.
func (h *Hub) addressAdmission(c peer.Contact, id string) string {
	if !dials(c, h.identity.Fingerprint()) {
		return peer.ErrNotDialing.Error()
	}
	return h.addresses.claim(c.IdentityKey, id)
}

// addressReplay answers a known operation from what is stored: an accepted
// one with the current snapshot, never applying its candidate again.
func (h *Hub) addressReplay(ctx context.Context, op store.AddressOp, outcome store.AddressOutcome) protocol.Message {
	switch {
	case outcome.State == store.AddressAccepted:
		return h.addressAccepted(ctx, op.ID, op.Contact)
	case outcome.State == store.AddressRefused:
		return addressRefused(op.ID, outcome.Reason)
	case h.addresses.runs(op.Contact, op.ID):
		return addressRefused(op.ID, addressRunning)
	}
	// Running in the store but not here: the broker stopped mid-probe.
	return h.refuseAddress(ctx, op.ID, addressInterrupted)
}

// refuseAddress settles a running operation as refused and answers so.
func (h *Hub) refuseAddress(ctx context.Context, id, reason string) protocol.Message {
	if err := h.store.RefuseContactAddress(ctx, id, reason); err != nil {
		return (&routingError{protocol.ErrInternal, "operator audit unavailable"}).frame(id)
	}
	return addressRefused(id, reason)
}

func addressRefused(id, reason string) protocol.Message {
	return (&routingError{protocol.ErrContactRefused, reason}).frame(id)
}

// addressAccepted is the answer to an accepted change: the snapshot as it
// is now, whatever the request asked for.
func (h *Hub) addressAccepted(ctx context.Context, id, contact string) protocol.Message {
	snapshot, rerr := h.applyPairing(ctx, pairingOps[protocol.PairingOpList], "")
	if rerr != nil {
		return rerr.frame(id)
	}
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: id, ContactKey: contact,
		ContactOp: protocol.ContactOpAddress, PairingResult: snapshot}
}

// addressCheck is one probe: the contact as read when it began, the
// candidate address, and this workspace's fingerprint for dial ownership.
type addressCheck struct {
	contact   peer.Contact
	candidate string
	own       string
}

// startAddressCheck probes the candidate off the Run goroutine, bounded by
// AttemptBudget and cancelled at shutdown, commits a proven address, and
// answers through the Run goroutine, which re-reads the contact.
func (h *Hub) startAddressCheck(req operatorRequest, check addressCheck) {
	cfg, runCtx, identity := h.pair.cfg, h.pair.runCtx, h.identity
	id, contact := req.msg.MessageID, check.contact.IdentityKey
	h.pair.workers.Add(1)
	go func() {
		defer h.pair.workers.Done()
		defer h.addresses.release(contact)
		netCtx, cancelNet := context.WithTimeout(runCtx, AttemptBudget)
		defer cancelNet()
		storeCtx, cancelStore := context.WithTimeout(context.WithoutCancel(runCtx), AttemptBudget+PairingGrace)
		defer cancelStore()
		reason := probeAddress(netCtx, addressProbe{cfg: cfg, identity: identity, pin: contact, candidate: check.candidate})
		finish := h.settleAddress(storeCtx, id, check, reason)
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

// probeAddress dials the candidate and completes a handshake pinned to the
// contact's identity, then closes: nothing is exchanged. It returns why the
// address can't be used, or "".
func probeAddress(ctx context.Context, p addressProbe) string {
	raw, err := p.cfg.Dial(ctx, p.candidate)
	if err != nil {
		return p.candidate + " is unreachable"
	}
	conn, err := firstcontact.MessageClient(ctx, raw, p.identity, p.cfg.Clock, p.pin)
	switch {
	case errors.Is(err, peer.ErrPeerMismatch):
		return fmt.Sprintf("the workspace at %s isn't them: identity mismatch", p.candidate)
	case err != nil:
		return p.candidate + " is unreachable"
	}
	conn.Close()
	return ""
}

// settleAddress stores the probe's outcome and returns the step that
// answers it on the Run goroutine.
func (h *Hub) settleAddress(ctx context.Context, id string, check addressCheck, reason string) func(context.Context) protocol.Message {
	if reason != "" {
		answer := h.refuseAddress(ctx, id, reason)
		return func(context.Context) protocol.Message { return answer }
	}
	c := check.contact
	_, err := h.store.ChangeContactAddress(ctx, store.ContactAddress{OpID: id, Contact: c.IdentityKey, From: c.Destination, FromAt: c.DestinationAt,
		To: check.candidate, At: h.contactNow(), StillDials: func(now peer.Contact) bool { return dials(now, check.own) }})
	switch {
	case errors.Is(err, peer.ErrDestinationChanged), errors.Is(err, peer.ErrNotDialing):
		answer := addressRefused(id, err.Error())
		return func(context.Context) protocol.Message { return answer }
	case err != nil:
		answer := (&routingError{protocol.ErrInternal, "address not stored"}).frame(id)
		return func(context.Context) protocol.Message { return answer }
	}
	return func(ctx context.Context) protocol.Message { return h.addressMoved(ctx, id, c.IdentityKey) }
}

// addressMoved, on the Run goroutine, adopts the contact's destination as
// stored now (a newer redemption may have moved it again), wakes its
// sync, and answers with the snapshot.
func (h *Hub) addressMoved(ctx context.Context, id, contact string) protocol.Message {
	h.readDestination(contact)
	h.PairingChanged()
	h.SyncMessages(contact)
	return h.addressAccepted(ctx, id, contact)
}
