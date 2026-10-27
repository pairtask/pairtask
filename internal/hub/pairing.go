package hub

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Operator pairing (#178). The invitation lives in this hub's memory only, so
// a broker restart drops it; decided requests are durable in the store. The
// code leaves the hub in exactly one frame, the response to op=open: it is
// never audited, emitted, logged or stored.

// pairingRefusals name each peer refusal on the wire.
var pairingRefusals = map[error]string{
	peer.ErrNoInvitation:        "no_invitation",
	peer.ErrInvitationExpired:   "invitation_expired",
	peer.ErrInvitationBusy:      "invitation_busy",
	peer.ErrInvitationExhausted: "invitation_exhausted",
	peer.ErrInvitationConsumed:  "invitation_consumed",
	peer.ErrInvitationCancelled: "invitation_cancelled",
	peer.ErrNotPending:          "not_pending",
	peer.ErrNoPairingRequest:    "no_pairing_request",
	peer.ErrOutcomeCapacity:     "outcome_capacity",
	peer.ErrIdentityCapacity:    "identity_capacity",
	peer.ErrTooManyPending:      "pending_capacity",
}

// identityProblems name why the workspace identity is unavailable; anything
// else, such as an unreadable marker, is identity_unavailable.
var identityProblems = map[error]string{
	peer.ErrIdentityMissing:  "identity_missing",
	peer.ErrIdentityCorrupt:  "identity_corrupt",
	peer.ErrIdentityUnsafe:   "identity_unsafe",
	peer.ErrIdentityMismatch: "identity_mismatch",
}

func identityProblem(err error) string {
	for known, reason := range identityProblems {
		if errors.Is(err, known) {
			return reason
		}
	}
	return "identity_unavailable"
}

func pairingError(err error) *routingError {
	for known, reason := range pairingRefusals {
		if errors.Is(err, known) {
			return &routingError{protocol.ErrPairing, reason}
		}
	}
	return &routingError{protocol.ErrInternal, "pairing state unavailable"}
}

// pairingOp checks a request's target and applies it; open returns the code.
// Only open needs the workspace identity: the others keep working without it,
// so the operator can still see and decide requests while repairing it.
type pairingOp struct {
	target   bool
	identity bool
	apply    func(h *Hub, ctx context.Context, target string) (code string, err error)
}

var pairingOps = map[string]pairingOp{
	protocol.PairingOpOpen: {false, true, func(h *Hub, _ context.Context, _ string) (string, error) {
		inv, err := h.pairing.Open()
		return inv.Code, err
	}},
	protocol.PairingOpCancel: {true, false, func(h *Hub, _ context.Context, id string) (string, error) {
		return "", h.pairing.Cancel(id)
	}},
	protocol.PairingOpStatus: {false, false, noPairingChange},
	protocol.PairingOpList:   {false, false, noPairingChange},
	protocol.PairingOpAccept: {true, false, func(h *Hub, ctx context.Context, key string) (string, error) {
		_, err := h.store.Accept(ctx, key)
		return "", err
	}},
	protocol.PairingOpReject: {true, false, func(h *Hub, ctx context.Context, key string) (string, error) {
		_, err := h.store.Reject(ctx, key)
		return "", err
	}},
}

func noPairingChange(*Hub, context.Context, string) (string, error) { return "", nil }

// ensurePairing creates the invitation manager on first use; the hub's Run
// goroutine serializes operator requests, so this needs no lock.
func (h *Hub) ensurePairing() error {
	if h.pairing != nil {
		return nil
	}
	manager, err := peer.NewInvitations(peer.DefaultInvitationPolicy(), time.Now, rand.Reader, h.store)
	h.pairing = manager
	return err
}

// loadIdentity loads, or for a new workspace creates, the identity key; a
// failure is retried on the next request, never repaired by replacing a key.
func (h *Hub) loadIdentity(ctx context.Context) string {
	if h.identity != nil {
		return ""
	}
	id, err := peer.LoadOrCreateIdentity(ctx, h.store.PeerDir(), h.store, rand.Reader)
	if err != nil {
		h.log.Warn("workspace identity unavailable", "err", err)
		return identityProblem(err)
	}
	h.identity = id
	return ""
}

func (h *Hub) operatorPairing(ctx context.Context, _ map[string]*Client, req operatorRequest) protocol.Message {
	msg := req.msg
	if len(msg.MessageID) > 128 {
		return (&routingError{protocol.ErrMalformedFrame, "message_id must be at most 128 bytes"}).frame("")
	}
	op, ok := pairingOps[msg.PairingOp]
	if !ok {
		return (&routingError{protocol.ErrMalformedFrame, "pairing_op must be open, cancel, status, list, accept or reject"}).frame(msg.MessageID)
	}
	if op.target != (msg.PairingTarget != "") {
		return (&routingError{protocol.ErrMalformedFrame, "cancel, accept and reject need pairing_target; the others take none"}).frame(msg.MessageID)
	}
	// The audit names the operation and its target, never the code.
	auditID, err := h.store.RecordOperatorAudit(ctx, store.OperatorAudit{Action: "pairing " + msg.PairingOp, PrincipalUID: req.uid,
		MessageID: msg.MessageID, Scope: msg.PairingTarget, Result: store.AuditPending})
	if err != nil {
		return (&routingError{protocol.ErrInternal, "operator audit unavailable; pairing unchanged"}).frame(msg.MessageID)
	}
	snapshot, rerr := h.applyPairing(ctx, op, msg.PairingTarget)
	if rerr != nil {
		h.finishAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditRejected, Detail: rerr.detail})
		return rerr.frame(msg.MessageID)
	}
	h.finishAudit(ctx, auditID, store.AuditOutcome{Result: store.AuditAccepted})
	return fitPairing(protocol.Message{Type: protocol.TypeOperatorPairingResult, MessageID: msg.MessageID, PairingResult: snapshot})
}

// fitPairing keeps whole requests, in order, while the response frame fits:
// a key or name is never shortened, and the total and the truncation flag
// keep the omission visible. The invitation, and open's code, always fit.
func fitPairing(resp protocol.Message) protocol.Message {
	snapshot := resp.PairingResult
	requests := snapshot.Requests
	snapshot.Requests = []protocol.PairingRequestView{}
	size := encodedLen(resp) + snapshotReserve
	for i, r := range requests {
		size += encodedLen(r) + 1 // and its comma
		if size > protocol.MaxFrameBytes {
			snapshot.RequestsTruncated = true
			break
		}
		snapshot.Requests = requests[:i+1]
	}
	return resp
}

// encodedLen is v's JSON size; what can't be encoded never fits.
func encodedLen(v any) int {
	data, err := json.Marshal(v)
	if err != nil {
		return protocol.MaxFrameBytes + 1
	}
	return len(data)
}

func (h *Hub) applyPairing(ctx context.Context, op pairingOp, target string) (*protocol.PairingSnapshot, *routingError) {
	if err := h.ensurePairing(); err != nil {
		return nil, &routingError{protocol.ErrInternal, "pairing unavailable"}
	}
	problem := h.loadIdentity(ctx)
	if op.identity && problem != "" {
		return nil, &routingError{protocol.ErrPairing, problem}
	}
	code, err := op.apply(h, ctx, target)
	if err != nil {
		return nil, pairingError(err)
	}
	snapshot, err := h.pairingSnapshot(ctx)
	if err != nil {
		return nil, pairingError(err)
	}
	if code != "" {
		snapshot.Invitation.Code = code
	}
	snapshot.IdentityError = problem
	if h.identity != nil {
		snapshot.SelfIdentity = h.identity.Fingerprint()
	}
	return snapshot, nil
}

// pairingSnapshot is the open invitation without its code, and the requests.
func (h *Hub) pairingSnapshot(ctx context.Context) (*protocol.PairingSnapshot, error) {
	requests, err := h.store.Requests(ctx)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(requests, pendingFirst)
	snapshot := &protocol.PairingSnapshot{Requests: []protocol.PairingRequestView{}, RequestsTotal: len(requests)}
	if len(requests) > protocol.ListedPairingRequests {
		requests, snapshot.RequestsTruncated = requests[:protocol.ListedPairingRequests], true
	}
	for _, r := range requests {
		snapshot.Requests = append(snapshot.Requests, protocol.PairingRequestView{IdentityKey: r.IdentityKey, Name: r.Name,
			State: string(r.State), FirstPairedAt: r.FirstPairedAt})
	}
	if status, ok := h.pairing.Status(); ok {
		snapshot.Invitation = &protocol.InvitationView{ID: status.ID, ExpiresAt: status.ExpiresAt, AttemptsLeft: status.AttemptsLeft,
			InFlight: status.InFlight, Consumed: status.Consumed, Expired: status.Expired}
	}
	return snapshot, nil
}

// pendingFirst orders pending requests first, then by identity key.
func pendingFirst(a, b peer.PairingRequest) int {
	ap, bp := a.State == peer.PairingPending, b.State == peer.PairingPending
	if ap != bp {
		if ap {
			return -1
		}
		return 1
	}
	return strings.Compare(a.IdentityKey, b.IdentityKey)
}
