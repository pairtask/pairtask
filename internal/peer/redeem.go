package peer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
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
	ex     Exchanger
	store  JoinStore
	self   *WorkspaceIdentity
	clock  func() time.Time
	random io.Reader
}

// NewJoiner returns a joiner; with a nil Exchanger every join is refused.
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
	digits, err := j.admit(in)
	if err != nil {
		return JoinRecord{}, err
	}
	f = boundedFrames{f}
	opID, err := randomOperationID(j.random)
	if err != nil {
		return JoinRecord{}, err
	}
	start := JoinStart{OperationID: opID, Joiner: j.self.Fingerprint(), Name: in.Name, At: j.clock()}
	if _, err := j.store.BeginJoin(ctx, start); err != nil {
		return JoinRecord{}, err
	}
	hello, err := j.correlate(ctx, f, start)
	if err != nil {
		return j.fail(ctx, opID, err)
	}
	b := ExchangeBinding{InvitationID: hello.InvitationID, JoinerOperationID: opID, InviterOperationID: hello.OperationID,
		Inviter: in.Connection.PeerLeaf, Joiner: start.Joiner, JoinerName: in.Name, ChannelBinding: in.Connection.ChannelBinding}
	persisted, err := j.exchange(ctx, f, b, digits)
	if !persisted {
		return j.fail(ctx, opID, errors.Join(ErrExchangeFailed, err))
	}
	if err != nil {
		return j.unknown(ctx, opID, err)
	}
	return j.settle(ctx, f, opID)
}

// admit refuses before anything is written or sent, and returns the code's
// digits.
func (j *Joiner) admit(in JoinInput) ([]byte, error) {
	if j.ex == nil {
		return nil, ErrExchangeUnavailable
	}
	if err := in.Connection.check(j.self.Fingerprint()); err != nil {
		return nil, err
	}
	if !validPairingName(in.Name) {
		return nil, ErrInvalidBinding
	}
	return normalizeCode(in.Code)
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
