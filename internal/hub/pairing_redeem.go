package hub

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Redemption (#178): the operator redeems another workspace's code. The hub
// records the redemption durably, answers at once, and a worker dials the
// destination, runs first contact and the exchange, and settles the record.
// The code lives only in that worker's memory: it is never audited, logged,
// stored or echoed.

const (
	// AttemptBudget bounds one redemption's or inbound connection's network
	// work, matching the invitation's attempt timeout.
	AttemptBudget = 30 * time.Second
	// PairingGrace is how long settling may outlast the network work,
	// including at shutdown.
	PairingGrace = 10 * time.Second
)

// PairingConfig wires redemption. A nil Dial or Exchanger makes redeem
// unavailable; the rest of pairing still works.
type PairingConfig struct {
	Dial      func(ctx context.Context, destination string) (net.Conn, error)
	Exchanger peer.Exchanger
	Name      string // the name we pair under; "workspace" if empty
	Clock     func() time.Time
}

// PairingEndpoint is what an inbound pairing connection needs, read as one
// snapshot.
type PairingEndpoint struct {
	Identity *peer.WorkspaceIdentity
	Inviter  *peer.Inviter
	Clock    func() time.Time
}

// pairingWiring is the hub's pairing state beyond the invitation itself.
// cfg is set before Run; joiner belongs to the Run goroutine; the rest is
// safe from any goroutine.
type pairingWiring struct {
	cfg       PairingConfig
	joiner    *peer.Joiner
	endpoint  atomic.Pointer[PairingEndpoint]
	redeeming atomic.Bool
	workers   sync.WaitGroup
	runCtx    context.Context
}

func defaultPairingConfig() PairingConfig {
	return PairingConfig{Name: "workspace", Clock: time.Now}
}

// SetPairing configures redemption and inbound pairing. Call it before Run.
func (h *Hub) SetPairing(cfg PairingConfig) {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Name == "" {
		cfg.Name = "workspace"
	}
	h.pair.cfg = cfg
}

// PairingEndpoint is the identity and inviter for an inbound pairing
// connection, once the workspace identity has loaded. Safe from any
// goroutine.
func (h *Hub) PairingEndpoint() (PairingEndpoint, error) {
	ep := h.pair.endpoint.Load()
	if ep == nil {
		return PairingEndpoint{}, peer.ErrIdentityUnavailable
	}
	return *ep, nil
}

// RecoverJoins marks redemptions interrupted before verification
// unverified. Call it once at startup, before Run and any listener.
func (h *Hub) RecoverJoins(ctx context.Context) (int, error) {
	return h.store.RecoverJoins(ctx, h.pair.cfg.Clock())
}

func (h *Hub) redeemAvailable() bool {
	return h.pair.cfg.Dial != nil && h.pair.cfg.Exchanger != nil
}

// publishIdentity builds the joiner and the inbound endpoint for a loaded
// identity; it runs in the Run goroutine.
func (h *Hub) publishIdentity(id *peer.WorkspaceIdentity) {
	cfg := h.pair.cfg
	if joiner, err := peer.NewJoiner(cfg.Exchanger, h.store, id, cfg.Clock, rand.Reader); err == nil {
		h.pair.joiner = joiner
	}
	if h.pairing == nil {
		return
	}
	if inviter, err := peer.NewInviter(cfg.Exchanger, h.pairing, id); err == nil {
		h.pair.endpoint.Store(&PairingEndpoint{Identity: id, Inviter: inviter, Clock: cfg.Clock})
	}
}

// beginPairing runs at Run's start. With pairing wired, loading the
// identity now lets inbound pairing work before any operator request;
// otherwise the first pairing request loads it.
func (h *Hub) beginPairing(ctx context.Context) {
	h.pair.runCtx = ctx
	if h.pair.cfg.Exchanger != nil {
		h.loadIdentity(ctx)
	}
}

// settlePairing ends redemption workers at shutdown: their network work is
// already cancelled with Run's context, and settling gets PairingGrace.
func (h *Hub) settlePairing() {
	done := make(chan struct{})
	go func() {
		h.pair.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(PairingGrace):
		h.log.Warn("pairing redemption still settling at shutdown")
	}
}

// redeemFields refuses the redemption fields on any other operation, before
// its audit, so a code can never be carried into one.
func redeemFields(msg protocol.Message) *routingError {
	fields := []string{msg.PairingDestination, msg.PairingCode, msg.PairingName}
	if msg.PairingOp != protocol.PairingOpRedeem && slices.ContainsFunc(fields, func(f string) bool { return f != "" }) {
		return &routingError{protocol.ErrMalformedFrame, "pairing_destination, pairing_code and pairing_name belong to redeem only"}
	}
	return nil
}

func (h *Hub) redeemRequest(msg protocol.Message) (peer.JoinRequest, *routingError) {
	req := peer.JoinRequest{OperationID: msg.MessageID, Destination: msg.PairingDestination, Name: msg.PairingName, Code: []byte(msg.PairingCode)}
	if req.Name == "" {
		req.Name = h.pair.cfg.Name
	}
	if msg.MessageID == "" {
		return req, &routingError{protocol.ErrMalformedFrame, "redeem needs a message_id"}
	}
	if peer.CheckDestination(req.Destination) != nil {
		return req, &routingError{protocol.ErrMalformedFrame, "invalid_destination: want host:port"}
	}
	if err := peer.CheckJoinRequest(req); err != nil {
		return req, &routingError{protocol.ErrMalformedFrame, "invalid redeem: the code must be 4 to 18 digits, the name at most 64 printable bytes"}
	}
	return req, nil
}

// operatorRedeem records a redemption and starts its worker. A known
// operation ID is a replay and only reports the stored state.
func (h *Hub) operatorRedeem(ctx context.Context, req operatorRequest) protocol.Message {
	msg := req.msg
	join, rerr := h.redeemRequest(msg)
	if rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	if known, found, err := h.store.GetJoin(ctx, join.OperationID); err != nil || found {
		return h.replayRedeem(known, join, err).frameOr(msg.MessageID, known)
	}
	if rerr := h.redeemAdmission(ctx); rerr != nil {
		return rerr.frame(msg.MessageID)
	}
	auditID, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "pairing redeem", PrincipalUID: req.uid,
		MessageID: msg.MessageID, Scope: join.Destination, Result: store.AuditPending})
	if err != nil {
		return (&routingError{protocol.ErrInternal, "operator audit unavailable; nothing redeemed"}).frame(msg.MessageID)
	}
	pending, rec, err := h.pair.joiner.Begin(ctx, join)
	if err != nil {
		rerr := pairingError(err)
		h.finishAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditRejected, Detail: rerr.detail})
		return rerr.frame(msg.MessageID)
	}
	if pending != nil {
		h.startRedeem(pending, join.Destination)
	}
	h.finishAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditAccepted})
	return joinResult(msg.MessageID, rec)
}

// redeemReply is a replay's answer: the stored join, or why not.
type redeemReply struct{ rerr *routingError }

func (r redeemReply) frameOr(messageID string, known peer.JoinRecord) protocol.Message {
	if r.rerr != nil {
		return r.rerr.frame(messageID)
	}
	return joinResult(messageID, known)
}

func (h *Hub) replayRedeem(known peer.JoinRecord, join peer.JoinRequest, err error) redeemReply {
	if err != nil {
		return redeemReply{&routingError{protocol.ErrInternal, "pairing state unavailable"}}
	}
	if known.Destination != join.Destination || known.JoinerName != join.Name {
		return redeemReply{&routingError{protocol.ErrPairing, "join_conflict"}}
	}
	return redeemReply{}
}

// redeemAdmission refuses a new redemption before anything is written.
func (h *Hub) redeemAdmission(ctx context.Context) *routingError {
	if !h.redeemAvailable() {
		return &routingError{protocol.ErrPairing, "redeem_unavailable"}
	}
	if problem := h.loadIdentity(ctx); problem != "" {
		return &routingError{protocol.ErrPairing, problem}
	}
	if h.pair.joiner == nil {
		return &routingError{protocol.ErrPairing, "redeem_unavailable"}
	}
	if h.pair.redeeming.Load() {
		return &routingError{protocol.ErrPairing, "redeem_busy"}
	}
	return nil
}

func joinResult(messageID string, r peer.JoinRecord) protocol.Message {
	view := joinView(r)
	return protocol.Message{Type: protocol.TypeOperatorPairingResult, MessageID: messageID, PairingJoin: &view}
}

func joinView(r peer.JoinRecord) protocol.PairingJoinView {
	return protocol.PairingJoinView{OperationID: r.OperationID, State: string(r.State), Destination: r.Destination, Name: r.JoinerName,
		Peer: r.Peer, InvitationID: r.InvitationID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// startRedeem runs one redemption off the Run goroutine. Its network work is
// bounded by AttemptBudget and cancelled at shutdown; its store writes are
// not cancelled with it, so the outcome is always settled durably.
func (h *Hub) startRedeem(pending *peer.PendingJoin, destination string) {
	ep := h.pair.endpoint.Load()
	cfg, runCtx := h.pair.cfg, h.pair.runCtx
	h.pair.redeeming.Store(true)
	h.pair.workers.Add(1)
	go func() {
		defer h.pair.workers.Done()
		defer h.pair.redeeming.Store(false)
		netCtx, cancelNet := context.WithTimeout(runCtx, AttemptBudget)
		defer cancelNet()
		storeCtx, cancelStore := context.WithTimeout(context.WithoutCancel(runCtx), AttemptBudget+PairingGrace)
		defer cancelStore()
		rec, err := redemption{cfg: cfg, ep: ep, pending: pending, destination: destination}.run(netCtx, storeCtx)
		if rec.OperationID == "" {
			h.log.Warn("pairing redemption not settled", "cause", PairingCause(err))
			return
		}
		h.log.Info("pairing redemption ended", "operation", rec.OperationID, "state", rec.State, "cause", PairingCause(err))
	}()
}

// pairingCauses name why a redemption or an inbound attempt ended, without
// any provider or peer text, which could carry anything.
var pairingCauses = []struct {
	err   error
	cause string
}{
	{peer.ErrJoinOutcomeUnknown, "outcome_unknown"},
	{peer.ErrJoinRefused, "refused"},
	{peer.ErrExchangeFailed, "exchange_failed"},
	{peer.ErrSelfPairing, "self_pairing"},
	{peer.ErrPeerMismatch, "peer_mismatch"},
	{peer.ErrPairingProtocol, "protocol_error"},
	{firstcontact.ErrFirstContact, "first_contact"},
	{peer.ErrNoInvitation, "no_invitation"},
	{peer.ErrInvitationExpired, "invitation_expired"},
	{peer.ErrInvitationBusy, "invitation_busy"},
	{peer.ErrInvitationExhausted, "invitation_exhausted"},
	{peer.ErrInvitationConsumed, "invitation_consumed"},
	{peer.ErrInvitationCancelled, "invitation_cancelled"},
	{peer.ErrExchangeUnavailable, "exchange_unavailable"},
	{context.DeadlineExceeded, "timeout"},
	{context.Canceled, "cancelled"},
}

// PairingCause is a fixed token for why pairing work ended, safe to log.
func PairingCause(err error) string {
	if err == nil {
		return "none"
	}
	for _, c := range pairingCauses {
		if errors.Is(err, c.err) {
			return c.cause
		}
	}
	return "other"
}

// redemption is one worker's redemption: where to dial and as whom.
type redemption struct {
	cfg         PairingConfig
	ep          *PairingEndpoint
	pending     *peer.PendingJoin
	destination string
}

func (r redemption) run(netCtx, storeCtx context.Context) (peer.JoinRecord, error) {
	if r.ep == nil {
		return r.pending.Abandon(storeCtx, peer.ErrIdentityUnavailable)
	}
	raw, err := r.cfg.Dial(netCtx, r.destination)
	if err != nil {
		return r.pending.Abandon(storeCtx, err)
	}
	conn, err := firstcontact.Client(netCtx, raw, r.ep.Identity, r.ep.Clock)
	if err != nil {
		return r.pending.Abandon(storeCtx, err)
	}
	defer conn.Close()
	return r.pending.Run(storeCtx, conn.Frames(netCtx), conn.Connection())
}

// joinsSnapshot adds this workspace's redemptions, newest first.
func (h *Hub) joinsSnapshot(ctx context.Context, snapshot *protocol.PairingSnapshot) error {
	joins, err := h.store.Joins(ctx)
	if err != nil {
		return err
	}
	slices.SortFunc(joins, newestJoinFirst)
	snapshot.Joins, snapshot.JoinsTotal = []protocol.PairingJoinView{}, len(joins)
	if len(joins) > protocol.ListedPairingJoins {
		joins, snapshot.JoinsTruncated = joins[:protocol.ListedPairingJoins], true
	}
	for _, j := range joins {
		snapshot.Joins = append(snapshot.Joins, joinView(j))
	}
	snapshot.RedeemAvailable = h.redeemAvailable()
	return nil
}

func newestJoinFirst(a, b peer.JoinRecord) int {
	if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
		return c
	}
	return strings.Compare(a.OperationID, b.OperationID)
}

// joinStatus is status for one redemption, listed or not.
func (h *Hub) joinStatus(ctx context.Context, opID string) (*protocol.PairingJoinView, *routingError) {
	r, found, err := h.store.GetJoin(ctx, opID)
	if err != nil {
		return nil, &routingError{protocol.ErrInternal, "pairing state unavailable"}
	}
	if !found {
		return nil, &routingError{protocol.ErrPairing, "no_join"}
	}
	view := joinView(r)
	return &view, nil
}
