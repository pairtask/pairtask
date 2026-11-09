package hub

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Settlement (#178): a worker settles joins with their inviters over pinned
// contact connections, one at a time, from a deduplicated, bounded queue.
// Automatic passes queue what still needs settling within SettleWindow, its
// budget and its backoff; an operator's settle queues at once, ignoring them.

const (
	SettleInterval    = 5 * time.Minute
	SettleWindow      = 7 * 24 * time.Hour
	SettleBackoffBase = time.Minute
	SettleBackoffMax  = time.Hour
	// SettleMaxAttempts bounds automatic passes for one join, pending replies
	// included; an operator's settle still tries, and resets it.
	SettleMaxAttempts = 12
	SettleQueue       = 64
)

// SettleSoon are the early automatic passes after a redemption (#284),
// measured from its end: the inviter usually accepts within a minute or
// two, and the operator should not wait for the next SettleInterval tick to
// see the contact. Each pass counts against SettleMaxAttempts and respects
// the backoff like any other.
var SettleSoon = []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute}

// settleSoon queues the join at each SettleSoon time while it still needs
// settling, then leaves it to the ticker.
func (h *Hub) settleSoon(ctx context.Context, id string) {
	after := h.pair.cfg.After
	if after == nil {
		after = time.After
	}
	h.pair.workers.Add(1)
	go func() {
		defer h.pair.workers.Done()
		var elapsed time.Duration
		for _, at := range SettleSoon {
			select {
			case <-ctx.Done():
				return
			case <-after(at - elapsed):
			}
			elapsed = at
			if !h.settleSoonPass(ctx, id) {
				return
			}
		}
	}()
}

// settleSoonPass queues one early pass; false once the join is settled,
// refused or gone. A backoff skips this slot, not the later ones.
func (h *Hub) settleSoonPass(ctx context.Context, id string) bool {
	r, found, err := h.store.GetJoin(ctx, id)
	if err != nil || !found || !h.liveCandidate(ctx, r) {
		return false
	}
	if h.pair.settle.due(id, h.pair.cfg.Clock()) {
		h.pair.settle.enqueue([]string{id}, true)
	}
	return true
}

// settleAttempt is one join's automatic retry state: every automatic pass
// counts against SettleMaxAttempts whatever its result, and only failures
// back off. An operator's settle resets both.
type settleAttempt struct {
	passes   int
	failures int
	next     time.Time
}

// settleItem is one queued join, and whether only an automatic pass asked
// for it.
type settleItem struct {
	id        string
	automatic bool
}

// settleState is the worker's queue and per-join results, safe from any
// goroutine. A join stays queued until its settlement finishes, so an active
// one is never queued twice.
type settleState struct {
	mu       sync.Mutex
	queue    []settleItem
	queued   map[string]bool
	attempts map[string]settleAttempt
	errors   map[string]string
	wake     chan struct{}
}

func (s *settleState) init() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queued == nil {
		s.queued, s.attempts, s.errors = map[string]bool{}, map[string]settleAttempt{}, map[string]string{}
		s.wake = make(chan struct{}, 1)
	}
}

// enqueue adds operation IDs once each, up to SettleQueue. An operator's
// settle (automatic false) resets their budget and backoff.
func (s *settleState) enqueue(ids []string, automatic bool) protocol.PairingSettleView {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	var view protocol.PairingSettleView
	for _, id := range ids {
		switch {
		case s.queued[id]:
			view.AlreadyQueued++
			s.claimManual(id, automatic)
		case len(s.queue) >= SettleQueue:
			view.NotQueued++
		default:
			s.queue, s.queued[id] = append(s.queue, settleItem{id, automatic}), true
			view.Queued++
		}
		if !automatic {
			delete(s.attempts, id)
		}
	}
	if view.Queued > 0 {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return view
}

// claimManual marks a waiting join as an operator's, so its pass is not
// counted against the automatic budget.
func (s *settleState) claimManual(id string, automatic bool) {
	if automatic {
		return
	}
	for i := range s.queue {
		if s.queue[i].id == id {
			s.queue[i].automatic = false
		}
	}
}

// next takes the oldest queued join; it stays marked queued until record.
func (s *settleState) next() (settleItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return settleItem{}, false
	}
	item := s.queue[0]
	s.queue = s.queue[1:]
	return item, true
}

// record finishes a settlement: it counts an automatic pass, keeps the fixed
// cause and backs off when it failed, and lets the join be queued again. It
// reports whether the shown cause changed or cleared.
func (s *settleState) record(item settleItem, cause string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.queued, item.id)
	shown := s.errors[item.id] != cause
	a := s.attempts[item.id]
	if item.automatic {
		a.passes++
	}
	if cause == "" {
		delete(s.errors, item.id)
		a.failures, a.next = 0, time.Time{}
	} else {
		s.errors[item.id] = cause
		a.failures++
		a.next = now.Add(min(SettleBackoffBase<<min(a.failures-1, 16), SettleBackoffMax))
	}
	s.attempts[item.id] = a
	return shown
}

func (s *settleState) failure(id string) string {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errors[id]
}

// due reports whether an automatic pass may try id now.
func (s *settleState) due(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attempts[id]
	return a.passes < SettleMaxAttempts && !now.Before(a.next)
}

// settleCandidate is a join that still needs settling and can be; one an
// unpair retired never does.
func settleCandidate(r peer.JoinRecord) bool {
	if r.Destination == "" || r.Peer == "" {
		return false
	}
	if !r.RetiredAt.IsZero() {
		return false
	}
	switch r.State {
	case peer.JoinOutcomeUnknown, peer.JoinAwaitingAcceptance:
		return true
	case peer.JoinContact, peer.JoinDeclined:
		return r.DecisionConfirmedAt.IsZero()
	}
	return false
}

// startSettleWorker runs the worker until ctx ends; shutdown waits for it
// with the redemption worker.
func (h *Hub) startSettleWorker(ctx context.Context) {
	s := &h.pair.settle
	s.init()
	tick := h.pair.cfg.SettleTick
	var ticker *time.Ticker
	if tick == nil {
		ticker = time.NewTicker(SettleInterval)
		tick = ticker.C
	}
	h.pair.workers.Add(1)
	go func() {
		defer h.pair.workers.Done()
		if ticker != nil {
			defer ticker.Stop()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick:
				h.queueAutomatic(ctx)
			case <-s.wake:
			}
			h.drainSettle(ctx)
		}
	}()
}

// queueAutomatic queues candidates within the window and their backoff.
func (h *Hub) queueAutomatic(ctx context.Context) {
	joins, err := h.store.Joins(ctx)
	if err != nil {
		h.log.Warn("pairing settlement pass skipped", "cause", "store")
		return
	}
	now := h.pair.cfg.Clock()
	var ids []string
	for _, r := range joins {
		if h.automaticDue(ctx, r, now) {
			ids = append(ids, r.OperationID)
		}
	}
	h.pair.settle.enqueue(ids, true)
}

// automaticDue reports a candidate within SettleWindow, its budget and its
// backoff.
func (h *Hub) automaticDue(ctx context.Context, r peer.JoinRecord, now time.Time) bool {
	if !h.liveCandidate(ctx, r) || now.Sub(r.CreatedAt) > SettleWindow {
		return false
	}
	return h.pair.settle.due(r.OperationID, now)
}

// liveCandidate is a candidate whose exchange can still install: one
// the ledger retired or refused, an address move included, is settled
// already (#276), and no ack loop retries it.
func (h *Hub) liveCandidate(ctx context.Context, r peer.JoinRecord) bool {
	if !r.Mode.Epoch() {
		return settleCandidate(r)
	}
	if !settleCandidate(r) {
		return false
	}
	x, found, err := h.store.Exchange(ctx, r.OperationID)
	if err != nil || !found {
		return false
	}
	return x.State == peer.ExchangeOpen || x.State == peer.ExchangeInstalled
}

func (h *Hub) drainSettle(ctx context.Context) {
	for ctx.Err() == nil {
		item, ok := h.pair.settle.next()
		if !ok {
			return
		}
		changed, err := h.settleOne(ctx, item.id)
		// A new or cleared cause is news too: a changed peer identity
		// leaves the join itself untouched.
		if h.pair.settle.record(item, settleCause(err), h.pair.cfg.Clock()) || changed {
			h.PairingChanged()
		}
	}
}

// settleOne settles one join over a pinned contact connection, and reports
// whether its durable state changed, even when a later step failed. Network
// work is bounded and cancelled at shutdown; store writes are not cut short.
func (h *Hub) settleOne(runCtx context.Context, id string) (bool, error) {
	ep := h.pair.endpoint.Load()
	if ep == nil || ep.Joiner == nil {
		return false, peer.ErrIdentityUnavailable
	}
	storeCtx, cancelStore := context.WithTimeout(context.WithoutCancel(runCtx), AttemptBudget+PairingGrace)
	defer cancelStore()
	r, err := h.settleableJoin(storeCtx, id)
	if err != nil {
		return false, err
	}
	err = h.settleWith(runCtx, storeCtx, ep, r)
	after, found, readErr := h.store.GetJoin(storeCtx, id)
	return readErr == nil && found && joinMoved(r, after), err
}

// settleableJoin is the stored join, if it has somewhere to settle.
func (h *Hub) settleableJoin(ctx context.Context, id string) (peer.JoinRecord, error) {
	r, found, err := h.store.GetJoin(ctx, id)
	if err != nil || !found {
		return r, errors.Join(peer.ErrNoJoin, err)
	}
	if r.Destination == "" || r.Peer == "" {
		return r, errNoDestination
	}
	return r, nil
}

func (h *Hub) settleWith(runCtx, storeCtx context.Context, ep *PairingEndpoint, r peer.JoinRecord) error {
	netCtx, cancelNet := context.WithTimeout(runCtx, AttemptBudget)
	defer cancelNet()
	raw, err := h.pair.cfg.Dial(netCtx, r.Destination)
	if err != nil {
		return err
	}
	conn, err := firstcontact.ContactClient(netCtx, raw, ep.Identity, ep.Clock, r.Peer)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = ep.Joiner.Settle(storeCtx, conn.Frames(netCtx), conn.Connection(), r.OperationID)
	return err
}

// joinMoved reports a settlement step made durable: a receipt, a decision
// (with its contact) or its confirmation.
func joinMoved(before, after peer.JoinRecord) bool {
	return before.State != after.State || !before.DecisionConfirmedAt.Equal(after.DecisionConfirmedAt)
}

// PairingChanged has the operator session told, once for any burst, that
// durable pairing state changed in the background. Safe from any goroutine.
func (h *Hub) PairingChanged() {
	h.fenceDials()
	h.pair.dirty.Store(true)
	select {
	case h.pair.changed <- struct{}{}:
	default:
	}
}

// announceBackground tells the operator session what changed in the
// background since the last time, on the Run goroutine: pairing state, and
// each contact whose messages changed.
func (h *Hub) announceBackground() {
	if h.pair.dirty.Swap(false) {
		h.emit(protocol.SessionEvent{Kind: protocol.EventPairingChanged})
	}
	h.announceMessages()
}

var errNoDestination = errors.New("hub: join has no destination or peer")

// settleCauses extend PairingCause with settlement's own fixed classes.
var settleCauses = []struct {
	err   error
	cause string
}{
	{errNoDestination, "no_destination"},
	{peer.ErrJoinConflict, "join_conflict"},
	{peer.ErrJoinState, "join_state"},
	{peer.ErrNoJoin, "no_join"},
	{peer.ErrContactCapacity, "contact_capacity"},
	{peer.ErrIdentityUnavailable, "identity_unavailable"},
}

func settleCause(err error) string {
	if err == nil {
		return ""
	}
	for _, c := range settleCauses {
		if errors.Is(err, c.err) {
			return c.cause
		}
	}
	return PairingCause(err)
}

// operatorSettle queues one join, or every candidate, after auditing the
// request, and answers with what it queued. Nothing here touches the
// network.
func (h *Hub) operatorSettle(ctx context.Context, req operatorRequest) protocol.Message {
	msg := req.msg
	if !h.redeemAvailable() {
		return (&routingError{protocol.ErrPairing, "redeem_unavailable"}).frame(msg.MessageID)
	}
	ids, rerr := h.settleTargets(ctx, msg.PairingTarget)
	if rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	scope := msg.PairingTarget
	if scope == "" {
		scope = "all"
	}
	auditID, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "pairing settle", PrincipalUID: req.uid,
		MessageID: msg.MessageID, Scope: scope, Result: store.AuditPending})
	if err != nil {
		return (&routingError{protocol.ErrInternal, "operator audit unavailable; nothing queued"}).frame(msg.MessageID)
	}
	view := h.pair.settle.enqueue(ids, false)
	h.finishAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditAccepted})
	resp, rerr := h.pairingResponse(ctx, pairingOps[protocol.PairingOpStatus], protocol.Message{
		MessageID: msg.MessageID, PairingOp: protocol.PairingOpStatus, PairingTarget: msg.PairingTarget})
	if rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	resp.PairingSettle = &view
	return fitPairing(resp)
}

// settleTargets are the named join, which must be settleable, or every
// current candidate regardless of window and backoff.
func (h *Hub) settleTargets(ctx context.Context, target string) ([]string, *routingError) {
	if target != "" {
		return h.settleTarget(ctx, target)
	}
	joins, err := h.store.Joins(ctx)
	if err != nil {
		return nil, &routingError{protocol.ErrInternal, "pairing state unavailable"}
	}
	var ids []string
	for _, r := range joins {
		if h.liveCandidate(ctx, r) {
			ids = append(ids, r.OperationID)
		}
	}
	return ids, nil
}

// settleTarget is the named join, if it can be settled.
func (h *Hub) settleTarget(ctx context.Context, target string) ([]string, *routingError) {
	r, found, err := h.store.GetJoin(ctx, target)
	switch {
	case err != nil:
		return nil, &routingError{protocol.ErrInternal, "pairing state unavailable"}
	case !found:
		return nil, &routingError{protocol.ErrPairing, "no_join"}
	case r.Destination == "" || r.Peer == "":
		return nil, &routingError{protocol.ErrPairing, "no_destination"}
	case r.State == peer.JoinAddressMoved:
		return nil, nil // settled by its receipt: nothing to queue
	}
	if rerr := h.settleEnded(ctx, r); rerr != nil {
		return nil, rerr
	}
	return []string{target}, nil
}

// endedExchanges are the ledger states nothing settles any more (#276).
var endedExchanges = map[string]routingError{
	peer.ExchangeRetired: {protocol.ErrPairing, "join_retired"},
	peer.ExchangeRefused: {protocol.ErrPairing, "join_refused"},
}

// settleEnded refuses a join an unpair retired, or whose ledger exchange
// was retired or refused, before any network work (#276).
func (h *Hub) settleEnded(ctx context.Context, r peer.JoinRecord) *routingError {
	if !r.RetiredAt.IsZero() {
		rerr := endedExchanges[peer.ExchangeRetired]
		return &rerr
	}
	x, found, err := h.store.Exchange(ctx, r.OperationID)
	if err != nil {
		return &routingError{protocol.ErrInternal, "pairing state unavailable"}
	}
	if rerr, ended := endedExchanges[x.State]; found && ended {
		return &rerr
	}
	return nil
}

// contactsSnapshot lists contacts newest first, each confirmed from the full
// durable state: a delivered acceptance as inviter, or a confirmed contact
// join as joiner. requests are all of them, not the listed ones.
func (h *Hub) contactsSnapshot(ctx context.Context, snapshot *protocol.PairingSnapshot, requests []peer.PairingRequest) error {
	contacts, err := h.store.Contacts(ctx)
	if err != nil {
		return err
	}
	joins, err := h.store.Joins(ctx)
	if err != nil {
		return err
	}
	confirmed := confirmedIdentities(requests, joins)
	counts, err := h.contactCounts(ctx)
	if err != nil {
		return err
	}
	slices.SortFunc(contacts, func(a, b peer.Contact) int { return b.EstablishedAt.Compare(a.EstablishedAt) })
	snapshot.Contacts, snapshot.ContactsTotal = []protocol.PairingContactView{}, len(contacts)
	if len(contacts) > protocol.ListedPairingContacts {
		contacts, snapshot.ContactsTruncated = contacts[:protocol.ListedPairingContacts], true
	}
	own := ""
	if h.identity != nil {
		own = h.identity.Fingerprint()
	}
	for _, c := range contacts {
		view := protocol.PairingContactView{IdentityKey: c.IdentityKey, Name: c.Name,
			Destination: c.Destination, EstablishedAt: c.EstablishedAt, Joined: c.Joined, Invited: c.Invited,
			Confirmed: confirmed[c.IdentityKey], LocalName: c.LocalName, Dials: own != "" && dials(c, own), PausedAt: pausedAt(c),
			Summary: &protocol.PairingContactSummary{Sync: h.contactSyncView(c.IdentityKey), ReviewsAwaiting: counts.awaiting[c.IdentityKey]},
			Acks:    ackView(counts.acks, c.IdentityKey)}
		noticeView(&view, counts.notices[c.IdentityKey])
		snapshot.Contacts = append(snapshot.Contacts, view)
	}
	return nil
}

// noticeView adds the contact's notice state to its view (#276): the
// peer's stored state, unavailable only once a version without notices is
// known, and this side's notice while the peer hasn't answered it.
func noticeView(view *protocol.PairingContactView, n store.ContactNoticeState) {
	view.PeerState = n.PeerState
	if !n.PeerStateAt.IsZero() {
		at := n.PeerStateAt
		view.PeerStateAt = &at
	}
	if n.PeerVersion > 0 && n.PeerVersion < int(messaging.PairingVersion) {
		view.PeerStateAvailable = "unavailable"
	}
	if n.Notice == "pending" || n.Notice == "undelivered" {
		view.Notice, view.NoticeAttempts = n.Notice, n.NoticeAttempts
	}
	pairingView(view, n)
}

// pairingView is the pairing the contact stands for (#276): its epoch,
// whether it is legacy, and when this side unpaired it.
func pairingView(view *protocol.PairingContactView, n store.ContactNoticeState) {
	view.PairingEpoch, view.Legacy = n.PairingEpoch, n.Legacy
	if !n.UnpairedAt.IsZero() {
		at := n.UnpairedAt
		view.UnpairedAt = &at
	}
}

// pausedAt is when this side paused the contact, nil while active (#276).
func pausedAt(c peer.Contact) *time.Time {
	if c.PausedAt.IsZero() {
		return nil
	}
	at := c.PausedAt
	return &at
}

// confirmedIdentities are those with a delivered acceptance or a confirmed
// contact join; one is enough.
func confirmedIdentities(requests []peer.PairingRequest, joins []peer.JoinRecord) map[string]bool {
	out := map[string]bool{}
	for _, r := range requests {
		out[r.IdentityKey] = out[r.IdentityKey] || deliveredAcceptance(r)
	}
	for _, j := range joins {
		out[j.Peer] = out[j.Peer] || confirmedContact(j)
	}
	return out
}

func deliveredAcceptance(r peer.PairingRequest) bool {
	return r.State == peer.PairingAccepted && !r.DeliveredAt.IsZero()
}

func confirmedContact(j peer.JoinRecord) bool {
	return j.State == peer.JoinContact && !j.DecisionConfirmedAt.IsZero()
}
