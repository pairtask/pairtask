package hub

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/egress"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Known-peer mobility (#284): a host helper leases what it may do, and hands
// over endpoints it found; this broker probes each one off the Run
// goroutine and moves a failing contact there only when the workspace
// there presents that contact's pinned identity. A move goes through the
// address operation's store fence, so a pause, a newer address or a route
// that works again since the probe began each keeps the contact as it is.

// candidateProbeBudget bounds one candidate's dial and handshake, so a
// whole operation (MaxCandidates of them, about 24s) ends within the
// operator CLI's longest wait, 30s. A LAN or tailnet endpoint answers a
// TCP and TLS 1.3 handshake in a few round trips.
const candidateProbeBudget = 3 * time.Second

// candidatesKey reserves the one candidates operation running at a time,
// in the address checks it shares the bound with.
const candidatesKey = "\x00candidates"

// mobilityOps are served by operatorMobility: neither names a contact.
var mobilityOps = map[string]bool{protocol.ContactOpDiscovery: true, protocol.ContactOpCandidates: true, protocol.ContactOpFind: true}

// operatorMobility serves discovery and candidates.
func (h *Hub) operatorMobility(ctx context.Context, req operatorRequest) protocol.Message {
	if problem := h.loadIdentity(ctx); problem != "" {
		return (&routingError{protocol.ErrInternal, "this workspace's identity is unavailable"}).frame(req.msg.MessageID)
	}
	switch req.msg.ContactOp {
	case protocol.ContactOpDiscovery:
		return h.discoveryLease(ctx, req.msg)
	case protocol.ContactOpFind:
		return h.operatorFind(ctx, req.msg)
	}
	return h.contactCandidates(ctx, req)
}

// mobilityRead is what the lease and the candidates read of the contacts:
// whether one may be looking for this workspace, and the dialled ones
// whose last attempt failed.
type mobilityRead struct {
	listened bool
	failing  []peer.Contact
}

// readMobility reads the confirmed, active contacts: one this side doesn't
// dial is listened for, one it dials and fails to reach is failing.
func (h *Hub) readMobility(ctx context.Context) (mobilityRead, error) {
	var r mobilityRead
	contacts, err := h.store.Contacts(ctx)
	if err != nil {
		return r, err
	}
	own := h.identity.Fingerprint()
	for _, c := range contacts {
		active, err := h.activeContact(ctx, c)
		if err != nil {
			return r, err
		}
		switch {
		case !active:
		case !dials(c, own):
			r.listened = true
		case h.failingRoute(c.IdentityKey):
			r.failing = append(r.failing, c)
		}
	}
	return r, nil
}

// activeContact: confirmed, not unpaired, not paused.
func (h *Hub) activeContact(ctx context.Context, c peer.Contact) (bool, error) {
	if !c.PausedAt.IsZero() {
		return false, nil
	}
	return h.store.PeerMessages().Confirmed(ctx, c.IdentityKey)
}

// failingRoute: the last attempt failed, no link is open, and no address
// check of the contact runs.
func (h *Hub) failingRoute(contact string) bool {
	s := h.MessageSyncStatus(contact)
	return s.LastFailure != "" && !s.Live && !h.addresses.checking(contact)
}

// discoveryLease answers discovery: what the helper may do until when. The
// request carries the helper's report, taken whole (#284); the lease is
// short while an invitation is announced or the operator is finding.
func (h *Hub) discoveryLease(ctx context.Context, msg protocol.Message) protocol.Message {
	now := h.contactNow()
	h.find.helperSeen(msg, now)
	r, err := h.readMobility(ctx)
	if err != nil {
		return (&routingError{protocol.ErrInternal, "contacts unavailable"}).frame(msg.MessageID)
	}
	browse := len(r.failing) > 0 && h.canDial()
	lease := protocol.ContactDiscovery{Advertise: r.listened, Browse: browse, Ports: []int{},
		Invitation: h.discoverableInvitation(), Find: h.find.wanted(now)}
	if browse {
		lease.Ports = failingPorts(r.failing)
	}
	lease.ExpiresAt = now.Add(leaseLength(lease))
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactOp: msg.ContactOp, ContactDiscovery: &lease}
}

// leaseLength: short while something the operator can cancel is going on.
func leaseLength(lease protocol.ContactDiscovery) time.Duration {
	if lease.Invitation != nil || lease.Find {
		return protocol.DiscoveryShortLease
	}
	return protocol.DiscoveryLease
}

// failingPorts are the distinct ports failing contacts were last reached
// on, sorted, at most DiscoveryPorts.
func failingPorts(failing []peer.Contact) []int {
	ports := []int{}
	for _, c := range failing {
		if _, port, err := peer.SplitDestination(c.Destination); err == nil && !slices.Contains(ports, int(port)) {
			ports = append(ports, int(port))
		}
	}
	slices.Sort(ports)
	return ports[:min(len(ports), protocol.DiscoveryPorts)]
}

// contactCandidates begins a candidates operation: answered at once when
// nothing is failing, otherwise by its worker.
func (h *Hub) contactCandidates(ctx context.Context, req operatorRequest) protocol.Message {
	msg := req.msg
	r, err := h.readMobility(ctx)
	if err != nil {
		return (&routingError{protocol.ErrInternal, "contacts unavailable"}).frame(msg.MessageID)
	}
	if len(r.failing) == 0 || !h.canDial() {
		return h.candidatesAnswer(ctx, msg, skippedAll(msg.ContactCandidates))
	}
	slot := addressRequest{id: msg.MessageID, contact: candidatesKey}
	if why := h.addresses.claim(slot); why != "" {
		return slot.refused(why)
	}
	h.startCandidates(req, candidateRun{failing: r.failing, own: h.identity.Fingerprint(), slot: slot, began: h.contactNow()})
	return answerLater
}

// skippedAll is every candidate skipped: nothing to look for.
func skippedAll(candidates []string) []protocol.CandidateResult {
	out := make([]protocol.CandidateResult, len(candidates))
	for i, c := range candidates {
		out[i] = protocol.CandidateResult{Candidate: c, Outcome: protocol.CandidateSkipped}
	}
	return out
}

// candidatesAnswer is the operation's one answer: the outcomes and the
// pairing snapshot as it is now.
func (h *Hub) candidatesAnswer(ctx context.Context, msg protocol.Message, results []protocol.CandidateResult) protocol.Message {
	snapshot, rerr := h.applyPairing(ctx, pairingOps[protocol.PairingOpList], pairingArgs{})
	if rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	return protocol.Message{Type: protocol.TypeOperatorContactResult, MessageID: msg.MessageID, ContactOp: msg.ContactOp,
		ContactCandidatesResult: results, PairingResult: snapshot}
}

// candidateRun is one candidates operation: the failing contacts as read
// when it began, and when that was, this workspace's fingerprint and its
// reserved slot.
type candidateRun struct {
	failing []peer.Contact
	began   time.Time
	own     string
	slot    addressRequest
	uid     int
	opID    string
}

// startCandidates probes the candidates in order off the Run goroutine,
// bounded by candidateProbeBudget each, and answers on the Run goroutine.
func (h *Hub) startCandidates(req operatorRequest, run candidateRun) {
	run.uid, run.opID = req.uid, req.msg.MessageID
	runCtx := h.pair.runCtx
	h.pair.workers.Add(1)
	go func() {
		defer h.pair.workers.Done()
		defer h.addresses.release(run.slot)
		results, moved := h.probeCandidates(runCtx, run, req.msg.ContactCandidates)
		h.finishOperator(runCtx, req, func(ctx context.Context) protocol.Message {
			h.candidatesMoved(moved)
			return h.candidatesAnswer(ctx, req.msg, results)
		})
	}()
}

// probeCandidates tries each candidate against the contacts still failing,
// and returns the outcomes and the contacts moved.
func (h *Hub) probeCandidates(runCtx context.Context, run candidateRun, candidates []string) ([]protocol.CandidateResult, []string) {
	results := make([]protocol.CandidateResult, len(candidates))
	var moved []string
	for i, candidate := range candidates {
		res := h.tryCandidate(runCtx, run, candidate, strconv.Itoa(i))
		if res.Outcome == protocol.CandidateAdopted {
			moved = append(moved, res.Contact)
			run.failing = slices.DeleteFunc(run.failing, func(c peer.Contact) bool { return c.IdentityKey == res.Contact })
		}
		results[i] = res
	}
	return results, moved
}

// tryCandidate probes one candidate against the contacts still failing now,
// then moves the contact it proved.
func (h *Hub) tryCandidate(runCtx context.Context, run candidateRun, candidate, n string) protocol.CandidateResult {
	res := protocol.CandidateResult{Candidate: candidate, Outcome: protocol.CandidateSkipped}
	failing := h.stillFailing(runCtx, run)
	if len(failing) == 0 {
		return res
	}
	found, outcome := h.probeCandidate(runCtx, failing, candidate)
	if outcome != "" {
		res.Outcome = outcome
		return res
	}
	res.Outcome = h.adoptCandidate(runCtx, run, found, candidateMove{candidate: candidate, opID: run.opID + "/" + n})
	if res.Outcome == protocol.CandidateAdopted {
		res.Contact = found.IdentityKey
	}
	return res
}

// probeCandidate dials the candidate and completes a handshake with
// whichever failing contact it presents, then closes: nothing is
// exchanged. It returns that contact, or the outcome instead.
func (h *Hub) probeCandidate(runCtx context.Context, failing []peer.Contact, candidate string) (peer.Contact, string) {
	ctx, cancel := context.WithTimeout(runCtx, candidateProbeBudget)
	defer cancel()
	cfg := h.pair.cfg
	raw, err := cfg.Dial(ctx, candidate)
	switch {
	case errors.Is(err, egress.ErrRefused):
		return peer.Contact{}, protocol.CandidateRefused
	case err != nil:
		return peer.Contact{}, protocol.CandidateUnreachable
	}
	conn, err := firstcontact.MessageClientAny(ctx, raw, h.identity, cfg.Clock, pinsOf(failing))
	switch {
	case errors.Is(err, peer.ErrPeerMismatch):
		return peer.Contact{}, protocol.CandidateNoMatch
	case err != nil:
		return peer.Contact{}, protocol.CandidateUnreachable
	}
	leaf := conn.Connection().PeerLeaf
	conn.Close()
	i := slices.IndexFunc(failing, func(c peer.Contact) bool { return c.IdentityKey == leaf })
	if i < 0 {
		return peer.Contact{}, protocol.CandidateNoMatch
	}
	return failing[i], ""
}

// stillFailing are the operation's contacts that may still authorize a
// probe: as stored now, active, in the pause generation the operation read,
// still failing, and not synced since it began. A paused, unpaired or
// recovered contact authorizes no further probe.
func (h *Hub) stillFailing(runCtx context.Context, run candidateRun) []peer.Contact {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(runCtx), PairingGrace)
	defer cancel()
	var out []peer.Contact
	for _, began := range run.failing {
		if h.mayProbe(ctx, run, began) {
			out = append(out, began)
		}
	}
	return out
}

// mayProbe: the contact still stands as when the operation began, and its
// route still fails.
func (h *Hub) mayProbe(ctx context.Context, run candidateRun, began peer.Contact) bool {
	now, ok := h.storedContact(ctx, began.IdentityKey)
	if !ok || !unchangedSince(began, now, run.own) {
		return false
	}
	active, err := h.activeContact(ctx, now)
	if err != nil || !active {
		return false
	}
	return h.failingRoute(began.IdentityKey) && !h.routeRecovered(began.IdentityKey, run.began)
}

// storedContact is the contact as stored now, if it can be read.
func (h *Hub) storedContact(ctx context.Context, key string) (peer.Contact, bool) {
	c, found, err := h.store.Contact(ctx, key)
	return c, err == nil && found
}

// pinsOf are the contacts' identity keys, in a fixed order.
func pinsOf(contacts []peer.Contact) []string {
	pins := make([]string, len(contacts))
	for i, c := range contacts {
		pins[i] = c.IdentityKey
	}
	slices.Sort(pins)
	return pins
}

// candidateMove is one proven candidate's move: where, and the address
// operation it is recorded as.
type candidateMove struct {
	candidate string
	opID      string
}

// adoptCandidate moves the contact to the candidate it proved, unless its
// own route works again since the operation began (a recovery during an
// earlier candidate counts for every later one), another address check of
// it runs, or the store's fence refuses: a pause, a newer address, or a
// side that no longer dials.
func (h *Hub) adoptCandidate(runCtx context.Context, run candidateRun, c peer.Contact, move candidateMove) string {
	r := addressRequest{id: move.opID, contact: c.IdentityKey, candidate: move.candidate}
	if h.routeRecovered(c.IdentityKey, run.began) || h.addresses.claim(r) != "" {
		return protocol.CandidateSkipped
	}
	defer h.addresses.release(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(runCtx), PairingGrace)
	defer cancel()
	_, known, err := h.store.BeginContactAddress(ctx, store.AddressOp{ID: r.id, Contact: r.contact, Candidate: r.candidate,
		At: h.contactNow(), PrincipalUID: run.uid})
	switch {
	case err != nil:
		return protocol.CandidateRefused
	case known:
		return protocol.CandidateSkipped // a repeated operation ID never moves a contact twice
	}
	_, err = h.store.ChangeContactAddress(ctx, store.ContactAddress{OpID: r.id, Contact: r.contact, From: c.Destination, FromAt: c.DestinationAt,
		To: r.candidate, At: h.contactNow(), StillDials: func(now peer.Contact) bool { return unchangedSince(c, now, run.own) }})
	if err != nil {
		_ = h.store.RefuseContactAddress(ctx, r.id, err.Error())
		return protocol.CandidateRefused
	}
	return protocol.CandidateAdopted
}

// unchangedSince: the contact as the store holds it now is still dialled by
// this side, active, and in the same pause generation as when the probe
// began, so a pause and resume meanwhile fences the move too.
func unchangedSince(began, now peer.Contact, own string) bool {
	return dials(now, own) && now.PausedAt.IsZero() && now.Generation == began.Generation
}

// routeRecovered: the contact's own route synced since the probe began.
func (h *Hub) routeRecovered(contact string, started time.Time) bool {
	s := h.MessageSyncStatus(contact)
	return s.Live || s.LastSuccess.After(started)
}

// candidatesMoved, on the Run goroutine, adopts each moved contact's
// destination as stored now and wakes its sync.
func (h *Hub) candidatesMoved(moved []string) {
	if len(moved) == 0 {
		return
	}
	for _, contact := range moved {
		h.readDestination(contact)
	}
	h.PairingChanged()
	for _, contact := range moved {
		h.SyncMessages(contact)
	}
}
