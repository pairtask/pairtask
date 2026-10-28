package peer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"
)

// Redemption (#178): the joiner opens with a hello, the inviter charges an
// attempt and answers with the invitation and its operation ID, the exchange
// runs, the inviter commits durably and only then sends its receipt.

var (
	// ErrJoinRefused: the inviter refused to start an attempt; see JoinRefusal.
	ErrJoinRefused = errors.New("peer: inviter refused the join")
	// ErrJoinOutcomeUnknown: our final confirmation may have reached the
	// inviter, but no receipt settled the join; it waits for reconciliation.
	ErrJoinOutcomeUnknown = errors.New("peer: join outcome unknown")
)

// JoinRefusal is the inviter's reason for refusing a join.
type JoinRefusal struct {
	Reason string
}

func (r *JoinRefusal) Error() string {
	return fmt.Sprintf("peer: inviter refused the join: %q", r.Reason)
}

func (r *JoinRefusal) Is(target error) bool { return target == ErrJoinRefused }

// refusalReasons name the inviter's refusals on the wire.
var refusalReasons = map[error]string{
	ErrNoInvitation:        "no_invitation",
	ErrInvitationExpired:   "invitation_expired",
	ErrInvitationBusy:      "invitation_busy",
	ErrInvitationExhausted: "invitation_exhausted",
	ErrInvitationConsumed:  "invitation_consumed",
	ErrInvitationCancelled: "invitation_cancelled",
}

func refusalReason(err error) string {
	for known, reason := range refusalReasons {
		if errors.Is(err, known) {
			return reason
		}
	}
	return "unavailable"
}

// Joiner redeems other workspaces' codes.
type Joiner struct {
	mu     sync.Mutex // serialises Begin's lookup and write
	ex     Exchanger
	store  JoinStore
	self   *WorkspaceIdentity
	clock  func() time.Time
	random io.Reader
}

// NewJoiner returns a joiner; with a nil Exchanger every join is refused.
// A store must have exactly one Joiner: Begin's replay guarantee holds only
// among calls on that one instance.
func NewJoiner(ex Exchanger, store JoinStore, self *WorkspaceIdentity, clock func() time.Time, random io.Reader) (*Joiner, error) {
	if slices.Contains([]bool{store != nil, self != nil, clock != nil, random != nil}, false) {
		return nil, errors.New("peer: a joiner needs a store, an identity, a clock and randomness")
	}
	return &Joiner{ex: ex, store: store, self: self, clock: clock, random: random}, nil
}

// JoinInput is what a join needs from the operator and the connection. The
// code is never formatted or encoded.
type JoinInput struct {
	Connection Connection
	Code       []byte `json:"-"`
	Name       string
}

func (in JoinInput) String() string {
	return fmt.Sprintf("peer.JoinInput{Peer: %q, Name: %q, Code: [redacted]}", in.Connection.PeerLeaf, in.Name)
}

func (in JoinInput) GoString() string { return in.String() }

// Join redeems a code on one connection. It writes the join before sending
// anything, and the authenticated inviter and transcript before our final
// confirmation can leave. With ErrJoinOutcomeUnknown the record is returned.
func (j *Joiner) Join(ctx context.Context, f Frames, in JoinInput) (JoinRecord, error) {
	if j.ex == nil {
		return JoinRecord{}, ErrExchangeUnavailable
	}
	if err := in.Connection.check(j.self.Fingerprint()); err != nil {
		return JoinRecord{}, err
	}
	opID, err := randomOperationID(j.random)
	if err != nil {
		return JoinRecord{}, err
	}
	pending, known, err := j.begin(ctx, JoinRequest{OperationID: opID, Name: in.Name, Code: in.Code})
	if err != nil {
		return JoinRecord{}, err
	}
	if pending == nil {
		// A drawn ID that already exists: never run someone else's join.
		return known, ErrJoinConflict
	}
	return pending.Run(ctx, f, in.Connection)
}

// JoinRequest is an operator's redemption, before any connection exists.
// OperationID is the operator's stable request ID; the code is never
// formatted, encoded or stored.
type JoinRequest struct {
	OperationID, Destination, Name string
	Code                           []byte `json:"-"`
}

func (r JoinRequest) String() string {
	return fmt.Sprintf("peer.JoinRequest{OperationID: %q, Destination: %q, Name: %q, Code: [redacted]}", r.OperationID, r.Destination, r.Name)
}

func (r JoinRequest) GoString() string { return r.String() }

// Begin records an operator's redemption durably, before any dial, and
// returns what will run it. A known operation ID is a replay: it returns the
// existing record and no PendingJoin, whatever code was supplied, since the
// code is never stored. A different destination or name is ErrJoinConflict.
func (j *Joiner) Begin(ctx context.Context, req JoinRequest) (*PendingJoin, JoinRecord, error) {
	if req.Destination == "" {
		return nil, JoinRecord{}, ErrInvalidDestination
	}
	return j.begin(ctx, req)
}

func (j *Joiner) begin(ctx context.Context, req JoinRequest) (*PendingJoin, JoinRecord, error) {
	// The lookup and the write are one step for this Joiner, so concurrent
	// calls for one operation ID get one PendingJoin between them. A store
	// has one Joiner: its broker's.
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ex == nil {
		return nil, JoinRecord{}, ErrExchangeUnavailable
	}
	digits, err := admitRequest(req)
	if err != nil {
		return nil, JoinRecord{}, err
	}
	start := JoinStart{OperationID: req.OperationID, Joiner: j.self.Fingerprint(), Name: req.Name, Destination: req.Destination, At: j.clock()}
	if known, found, err := j.store.GetJoin(ctx, req.OperationID); err != nil || found {
		clear(digits)
		return nil, known, replayed(known, start, err)
	}
	r, err := j.store.BeginJoin(ctx, start)
	if err != nil {
		clear(digits)
		return nil, JoinRecord{}, err
	}
	return &PendingJoin{j: j, start: start, digits: digits}, r, nil
}

func replayed(known JoinRecord, start JoinStart, err error) error {
	if err != nil {
		return err
	}
	_, err = ReplayJoin(known, start)
	return err
}

// CheckJoinRequest validates an operator's redemption without recording it:
// a destination, the operation ID, the name and the code.
func CheckJoinRequest(req JoinRequest) error {
	if err := CheckDestination(req.Destination); err != nil {
		return err
	}
	digits, err := admitRequest(req)
	clear(digits)
	return err
}

// admitRequest checks a request's own fields and returns the code's digits.
func admitRequest(req JoinRequest) ([]byte, error) {
	if !validPairingID(req.OperationID) || !validPairingName(req.Name) {
		return nil, ErrInvalidBinding
	}
	if err := validDestination(req.Destination); err != nil {
		return nil, err
	}
	return normalizeCode(req.Code)
}

// PendingJoin is a recorded redemption waiting for its connection. It runs
// or is abandoned exactly once, and forgets the code either way.
type PendingJoin struct {
	j     *Joiner
	start JoinStart

	mu     sync.Mutex
	digits []byte
	done   bool
}

// take hands out the code once.
func (p *PendingJoin) take() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return nil, ErrJoinState
	}
	p.done = true
	digits := p.digits
	p.digits = nil
	return digits, nil
}

// Run redeems on the connection the operator's destination produced. ctx
// bounds the store writes; f carries its own network deadline.
func (p *PendingJoin) Run(ctx context.Context, f Frames, c Connection) (JoinRecord, error) {
	digits, err := p.take()
	if err != nil {
		return JoinRecord{}, err
	}
	defer clear(digits)
	if err := c.check(p.start.Joiner); err != nil {
		return p.j.fail(ctx, p.start.OperationID, err)
	}
	return p.exchangeOn(ctx, boundedFrames{f}, c, digits)
}

// Abandon fails a redemption that never reached its inviter, such as a
// failed dial or handshake: nothing was sent, so it can't have been
// committed.
func (p *PendingJoin) Abandon(ctx context.Context, cause error) (JoinRecord, error) {
	digits, err := p.take()
	if err != nil {
		return JoinRecord{}, err
	}
	clear(digits)
	return p.j.fail(ctx, p.start.OperationID, cause)
}

// exchangeOn is the exchange after the intent was written.
func (p *PendingJoin) exchangeOn(ctx context.Context, f Frames, c Connection, digits []byte) (JoinRecord, error) {
	j, start := p.j, p.start
	opID := start.OperationID
	hello, err := j.correlate(ctx, f, start)
	if err != nil {
		return j.fail(ctx, opID, err)
	}
	b := ExchangeBinding{InvitationID: hello.InvitationID, JoinerOperationID: opID, InviterOperationID: hello.OperationID,
		Inviter: c.PeerLeaf, Joiner: start.Joiner, JoinerName: start.Name, ChannelBinding: c.ChannelBinding}
	persisted, err := j.exchange(ctx, f, b, digits)
	if !persisted {
		return j.fail(ctx, opID, errors.Join(ErrExchangeFailed, err))
	}
	if err != nil {
		return j.unknown(ctx, opID, err)
	}
	return j.settle(ctx, f, opID)
}

// Code bounds match InvitationPolicy's; typed input, grouping included, is
// bounded before it is scanned.
const (
	minCodeDigits = 4
	maxCodeDigits = 18
	maxCodeInput  = 64
)

// normalizeCode drops the grouping a person may type ("482-913", "482 913")
// so the exchange always binds the bare digits the inviter holds.
func normalizeCode(code []byte) ([]byte, error) {
	if len(code) > maxCodeInput {
		return nil, ErrInvalidBinding
	}
	digits := make([]byte, 0, maxCodeInput)
	for _, c := range code {
		keep, ok := codeByte(c)
		if !ok {
			return nil, ErrInvalidBinding
		}
		digits = append(digits, keep...)
	}
	if len(digits) < minCodeDigits || len(digits) > maxCodeDigits {
		return nil, ErrInvalidBinding
	}
	return digits, nil
}

// codeByte is what one typed byte contributes: a digit, nothing for
// grouping, or not ok.
func codeByte(c byte) ([]byte, bool) {
	if c >= '0' && c <= '9' {
		return []byte{c}, true
	}
	return nil, c == '-' || c == ' '
}

func randomOperationID(random io.Reader) (string, error) {
	var id [16]byte
	if _, err := io.ReadFull(random, id[:]); err != nil {
		return "", fmt.Errorf("peer: operation ID: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

// correlate sends our hello and records the inviter's answer.
func (j *Joiner) correlate(ctx context.Context, f Frames, start JoinStart) (InviterHello, error) {
	frame, err := EncodeJoinerHello(JoinerHello{OperationID: start.OperationID, Name: start.Name})
	if err != nil {
		return InviterHello{}, err
	}
	if err := f.Send(frame); err != nil {
		return InviterHello{}, err
	}
	hello, err := readInviterHello(f)
	if err != nil {
		return InviterHello{}, err
	}
	_, err = j.store.SetCorrelation(ctx, start.OperationID, hello, j.clock())
	return hello, err
}

// readInviterHello reads the inviter's hello, or its refusal as an error.
func readInviterHello(f Frames) (InviterHello, error) {
	frame, err := f.Recv()
	if err != nil {
		return InviterHello{}, err
	}
	kind, err := FrameKind(frame)
	if err != nil {
		return InviterHello{}, err
	}
	if kind == FrameRefusal {
		refusal, err := DecodeRefusal(frame)
		if err != nil {
			return InviterHello{}, err
		}
		return InviterHello{}, &JoinRefusal{Reason: refusal.Reason}
	}
	return DecodeInviterHello(frame)
}

// exchange runs the joiner's side, persisting what it authenticated before
// the final confirmation; persisted says whether that write happened.
func (j *Joiner) exchange(ctx context.Context, f Frames, b ExchangeBinding, code []byte) (persisted bool, err error) {
	if err := b.Validate(); err != nil {
		return false, err
	}
	err = j.ex.Join(ctx, f, b, code, func(ctx context.Context, res ExchangeResult) error {
		if res.Peer != b.Inviter {
			return ErrPeerMismatch
		}
		if _, err := j.store.RecordVerified(ctx, b.JoinerOperationID, res, j.clock()); err != nil {
			return err
		}
		persisted = true
		return nil
	})
	return persisted, err
}

// settle reads the inviter's receipt; anything short of an exactly matching
// one leaves the join unknown.
func (j *Joiner) settle(ctx context.Context, f Frames, opID string) (JoinRecord, error) {
	frame, err := f.Recv()
	if err != nil {
		return j.unknown(ctx, opID, err)
	}
	receipt, err := DecodeReceipt(frame)
	if err != nil {
		return j.unknown(ctx, opID, err)
	}
	r, err := j.store.RecordReceipt(ctx, opID, receipt, j.clock())
	if err != nil {
		return j.unknown(ctx, opID, err)
	}
	return r, nil
}

func (j *Joiner) fail(ctx context.Context, opID string, cause error) (JoinRecord, error) {
	r, err := j.store.FailJoin(ctx, opID, j.clock())
	return r, errors.Join(cause, err)
}

func (j *Joiner) unknown(ctx context.Context, opID string, cause error) (JoinRecord, error) {
	r, _, err := j.store.GetJoin(ctx, opID)
	return r, errors.Join(fmt.Errorf("%w: %w", ErrJoinOutcomeUnknown, cause), err)
}

// Inviter answers joins of this workspace's open invitation.
type Inviter struct {
	ex   Exchanger
	inv  *Invitations
	self *WorkspaceIdentity
}

// NewInviter returns an inviter; with a nil Exchanger every join is refused.
func NewInviter(ex Exchanger, inv *Invitations, self *WorkspaceIdentity) (*Inviter, error) {
	if inv == nil || self == nil {
		return nil, errors.New("peer: an inviter needs invitations and an identity")
	}
	return &Inviter{ex: ex, inv: inv, self: self}, nil
}

// Serve answers one join. An attempt is charged only after a well-formed
// hello; the receipt is sent only after the outcome is durably committed.
func (i *Inviter) Serve(ctx context.Context, f Frames, c Connection) (PairingRequest, error) {
	if i.ex == nil {
		return PairingRequest{}, ErrExchangeUnavailable
	}
	if err := c.check(i.self.Fingerprint()); err != nil {
		return PairingRequest{}, err
	}
	f = boundedFrames{f}
	hello, err := readJoinerHello(f)
	if err != nil {
		return PairingRequest{}, err
	}
	attempt, invitationID, err := i.begin(f, hello)
	if err != nil {
		return PairingRequest{}, err
	}
	b := ExchangeBinding{InvitationID: invitationID, JoinerOperationID: hello.OperationID, InviterOperationID: attempt.ID(),
		Inviter: i.self.Fingerprint(), Joiner: c.PeerLeaf, JoinerName: hello.Name, ChannelBinding: c.ChannelBinding}
	res, err := i.exchange(ctx, f, attempt, b)
	if err != nil {
		_ = attempt.Fail()
		return PairingRequest{}, err
	}
	request, err := confirm(ctx, attempt, PairedPeer{IdentityKey: res.Peer, Name: b.JoinerName, TranscriptID: res.TranscriptID})
	if err != nil {
		return PairingRequest{}, err
	}
	return request, sendReceipt(f, b, res)
}

// maxConfirmTries bounds retrying a commit the store refused. Every try
// commits the same frozen outcome; nothing reruns the exchange or Begin.
const maxConfirmTries = 3

// confirm commits the verified joiner. If every try fails, the attempt is
// left as Attempt.Confirm leaves it, still retryable until its deadline,
// and no receipt is sent.
func confirm(ctx context.Context, attempt *Attempt, p PairedPeer) (PairingRequest, error) {
	var request PairingRequest
	var err error
	for range maxConfirmTries {
		request, err = attempt.Confirm(ctx, p)
		if err == nil || errors.Is(err, ErrAttemptDone) {
			break
		}
	}
	return request, err
}

func readJoinerHello(f Frames) (JoinerHello, error) {
	frame, err := f.Recv()
	if err != nil {
		return JoinerHello{}, err
	}
	hello, err := DecodeJoinerHello(frame)
	if err != nil {
		return JoinerHello{}, err
	}
	if !validPairingID(hello.OperationID) || !validPairingName(hello.Name) {
		return JoinerHello{}, fmt.Errorf("%w: invalid hello", ErrPairingProtocol)
	}
	return hello, nil
}

// begin charges an attempt at the open invitation and answers the hello; a
// refusal is sent with its reason and charges nothing.
func (i *Inviter) begin(f Frames, hello JoinerHello) (*Attempt, string, error) {
	status, ok := i.inv.Status()
	if !ok {
		return nil, "", i.refuse(f, ErrNoInvitation)
	}
	attempt, err := i.inv.Begin(status.ID)
	if err != nil {
		return nil, "", i.refuse(f, err)
	}
	frame, err := EncodeInviterHello(InviterHello{InvitationID: status.ID, OperationID: attempt.ID()})
	if err == nil {
		err = f.Send(frame)
	}
	if err != nil {
		_ = attempt.Fail()
		return nil, "", err
	}
	return attempt, status.ID, nil
}

func (i *Inviter) refuse(f Frames, cause error) error {
	frame, err := EncodeRefusal(Refusal{Reason: refusalReason(cause)})
	if err == nil {
		err = f.Send(frame)
	}
	return errors.Join(cause, err)
}

// exchange runs the inviter's side and checks who it authenticated.
func (i *Inviter) exchange(ctx context.Context, f Frames, attempt *Attempt, b ExchangeBinding) (ExchangeResult, error) {
	if err := b.Validate(); err != nil {
		return ExchangeResult{}, err
	}
	res, err := i.ex.Invite(ctx, f, b, attempt.Code())
	if err != nil {
		return ExchangeResult{}, errors.Join(ErrExchangeFailed, err)
	}
	if res.Peer != b.Joiner {
		return ExchangeResult{}, ErrPeerMismatch
	}
	return res, nil
}

// sendReceipt tells the joiner what was committed, after the commit.
func sendReceipt(f Frames, b ExchangeBinding, res ExchangeResult) error {
	frame, err := EncodeReceipt(JoinReceipt{InvitationID: b.InvitationID, InviterOperationID: b.InviterOperationID,
		Joiner: b.Joiner, TranscriptID: res.TranscriptID})
	if err != nil {
		return err
	}
	return f.Send(frame)
}
