package peer

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"
)

// Pairing invitations (#178): the operator opens one, shares its short code
// over another medium, and the colleague's workspace proves knowledge of the
// code in a PAKE exchange (not part of this package yet). Invitations live in
// memory only: a broker restart invalidates every open one, so the code never
// touches disk and a restart cannot reset the guessing budget. Committed
// outcomes are durable in the PairingStore.

var (
	ErrNoInvitation        = errors.New("peer: no such open invitation")
	ErrInvitationExpired   = errors.New("peer: invitation expired")
	ErrInvitationBusy      = errors.New("peer: another pairing attempt is in progress")
	ErrInvitationExhausted = errors.New("peer: invitation has no attempts left")
	ErrInvitationConsumed  = errors.New("peer: invitation already used")
	ErrInvitationCancelled = errors.New("peer: invitation cancelled or replaced")
	// ErrAttemptDone means the attempt already ended: failed, timed out or committed.
	ErrAttemptDone = errors.New("peer: pairing attempt already ended")
)

// InvitationPolicy bounds an invitation.
type InvitationPolicy struct {
	Digits         int           // code length
	MaxAttempts    int           // attempts charged per invitation
	TTL            time.Duration // invitation lifetime
	AttemptTimeout time.Duration // one attempt's lifetime
}

// DefaultInvitationPolicy is 6 digits, 3 attempts, 10 minutes, 30 seconds.
func DefaultInvitationPolicy() InvitationPolicy {
	return InvitationPolicy{Digits: 6, MaxAttempts: 3, TTL: 10 * time.Minute, AttemptTimeout: 30 * time.Second}
}

func (p InvitationPolicy) validate() error {
	valid := []bool{
		p.Digits >= 4, p.Digits <= 18, // fits a uint64 draw
		p.MaxAttempts >= 1,
		p.TTL > 0,
		p.AttemptTimeout > 0,
	}
	if slices.Contains(valid, false) {
		return fmt.Errorf("peer: invalid invitation policy %+v", p)
	}
	return nil
}

// Invitation is a newly opened invitation. Code exists only here: it is meant
// for the operator's own UI response and must never become a broker message.
type Invitation struct {
	ID        string
	Code      string `json:"-"` // grouped for reading, e.g. "482-913"; never serialized
	ExpiresAt time.Time
}

// InvitationStatus describes the open invitation without its code.
type InvitationStatus struct {
	ID           string
	ExpiresAt    time.Time
	AttemptsLeft int
	InFlight     bool
	Consumed     bool
	Expired      bool
}

type invitation struct {
	id           string
	code         string // digits only
	expiresAt    time.Time
	attemptsLeft int
	inFlight     *Attempt
	consumed     bool
}

// Invitations holds at most one open invitation.
type Invitations struct {
	mu       sync.Mutex
	policy   InvitationPolicy
	clock    func() time.Time
	random   io.Reader
	store    PairingStore
	current  *invitation
	lastSeen time.Time
}

// NewInvitations returns a manager with no open invitation.
func NewInvitations(policy InvitationPolicy, clock func() time.Time, random io.Reader, store PairingStore) (*Invitations, error) {
	if err := policy.validate(); err != nil {
		return nil, err
	}
	if missing := slices.Contains([]bool{clock != nil, random != nil, store != nil}, false); missing {
		return nil, errors.New("peer: invitations need a clock, randomness and a pairing store")
	}
	return &Invitations{policy: policy, clock: clock, random: random, store: store}, nil
}

// now reads the clock, failing closed if it went backwards: a rollback must
// never extend an invitation or attempt, so it ends the open invitation.
func (iv *Invitations) now() (time.Time, bool) {
	t := iv.clock()
	if t.Before(iv.lastSeen) {
		iv.close()
		return t, false
	}
	iv.lastSeen = t
	return t, true
}

// close drops the current invitation and ends its attempt.
func (iv *Invitations) close() {
	if iv.current != nil && iv.current.inFlight != nil {
		iv.current.inFlight.state = attemptFailed
	}
	iv.current = nil
}

// settle fails an in-flight attempt that ran past its own deadline or the
// invitation's expiry; it stays charged.
func (iv *Invitations) settle(now time.Time) {
	inv := iv.current
	if inv == nil || inv.inFlight == nil {
		return
	}
	if !now.Before(inv.inFlight.deadline) || !now.Before(inv.expiresAt) {
		inv.inFlight.state = attemptFailed
		inv.inFlight = nil
	}
}

func (iv *Invitations) randomID() (string, error) {
	id := make([]byte, 16)
	if _, err := io.ReadFull(iv.random, id); err != nil {
		return "", fmt.Errorf("peer: randomness: %w", err)
	}
	return hex.EncodeToString(id), nil
}

// randomCode draws a uniform code of the policy's length by rejection sampling.
func (iv *Invitations) randomCode() (string, error) {
	limit := uint64(1)
	for range iv.policy.Digits {
		limit *= 10
	}
	bound := ^uint64(0) - ^uint64(0)%limit // largest multiple of limit, avoiding modulo bias
	var raw [8]byte
	for {
		if _, err := io.ReadFull(iv.random, raw[:]); err != nil {
			return "", fmt.Errorf("peer: randomness: %w", err)
		}
		if v := binary.BigEndian.Uint64(raw[:]); v < bound {
			return fmt.Sprintf("%0*d", iv.policy.Digits, v%limit), nil
		}
	}
}

// grouped splits a code into halves for reading aloud, e.g. "482-913".
func grouped(code string) string {
	half := (len(code) + 1) / 2
	return code[:half] + "-" + code[half:]
}

// Open replaces any open invitation, including one with an attempt in flight.
// On a randomness failure nothing changes.
func (iv *Invitations) Open() (Invitation, error) {
	iv.mu.Lock()
	defer iv.mu.Unlock()
	now, ok := iv.now()
	if !ok {
		return Invitation{}, ErrInvitationExpired
	}
	id, err := iv.randomID()
	if err != nil {
		return Invitation{}, err
	}
	code, err := iv.randomCode()
	if err != nil {
		return Invitation{}, err
	}
	iv.close()
	iv.current = &invitation{id: id, code: code, expiresAt: now.Add(iv.policy.TTL), attemptsLeft: iv.policy.MaxAttempts}
	return Invitation{ID: id, Code: grouped(code), ExpiresAt: iv.current.expiresAt}, nil
}

// Cancel ends the open invitation and any attempt in flight.
func (iv *Invitations) Cancel(invitationID string) error {
	iv.mu.Lock()
	defer iv.mu.Unlock()
	if iv.current == nil || iv.current.id != invitationID {
		return ErrNoInvitation
	}
	iv.close()
	return nil
}

// Status describes the open invitation, if any.
func (iv *Invitations) Status() (InvitationStatus, bool) {
	iv.mu.Lock()
	defer iv.mu.Unlock()
	now, ok := iv.now()
	if !ok || iv.current == nil {
		return InvitationStatus{}, false
	}
	iv.settle(now)
	inv := iv.current
	return InvitationStatus{ID: inv.id, ExpiresAt: inv.expiresAt, AttemptsLeft: inv.attemptsLeft,
		InFlight: inv.inFlight != nil, Consumed: inv.consumed, Expired: !now.Before(inv.expiresAt)}, true
}

// Begin starts one attempt, charging it before returning. Refusals are not
// charged.
func (iv *Invitations) Begin(invitationID string) (*Attempt, error) {
	iv.mu.Lock()
	defer iv.mu.Unlock()
	now, ok := iv.now()
	if !ok {
		return nil, ErrInvitationExpired
	}
	inv := iv.current
	if inv == nil || inv.id != invitationID {
		return nil, ErrNoInvitation
	}
	iv.settle(now)
	if err := inv.refusal(now); err != nil {
		return nil, err
	}
	id, err := iv.randomID()
	if err != nil {
		return nil, err
	}
	inv.attemptsLeft--
	attempt := &Attempt{iv: iv, inv: inv, id: id, deadline: now.Add(iv.policy.AttemptTimeout)}
	inv.inFlight = attempt
	return attempt, nil
}

// refusal is why a new attempt can't start, checked in order of finality.
func (inv *invitation) refusal(now time.Time) error {
	switch {
	case inv.consumed:
		return ErrInvitationConsumed
	case !now.Before(inv.expiresAt):
		return ErrInvitationExpired
	case inv.inFlight != nil:
		return ErrInvitationBusy
	case inv.attemptsLeft == 0:
		return ErrInvitationExhausted
	}
	return nil
}

type attemptState int

const (
	attemptOpen attemptState = iota
	attemptFailed
	attemptCommitted
)

// Attempt is one charged try at an invitation.
type Attempt struct {
	iv       *Invitations
	inv      *invitation
	id       string
	deadline time.Time
	state    attemptState
	outcome  *PairingOutcome // frozen at the first Confirm, so a retry can't change it
}

// ID is the attempt's operation ID, the key of its durable outcome.
func (a *Attempt) ID() string { return a.id }

// Code is the invitation code for the PAKE, as a copy.
func (a *Attempt) Code() []byte { return []byte(a.inv.code) }

// Fail ends the attempt without refunding it.
func (a *Attempt) Fail() error {
	a.iv.mu.Lock()
	defer a.iv.mu.Unlock()
	if a.state != attemptOpen {
		return ErrAttemptDone
	}
	a.end(attemptFailed)
	return nil
}

func (a *Attempt) end(state attemptState) {
	a.state = state
	if a.inv.inFlight == a {
		a.inv.inFlight = nil
	}
}

// PairedPeer is what a confirmed exchange established about the peer.
type PairedPeer struct {
	IdentityKey  string
	Name         string
	TranscriptID []byte
}

func (p PairedPeer) matches(o PairingOutcome) bool {
	return p.IdentityKey == o.IdentityKey && p.Name == o.Name && bytes.Equal(p.TranscriptID, o.TranscriptID)
}

// Confirm commits the pairing and consumes the invitation, in that order and
// under one lock: nothing is consumed or reported until the store has the
// outcome. After a store error the attempt stays open, and only identical
// content may be retried before its deadline; there is no refund.
func (a *Attempt) Confirm(ctx context.Context, p PairedPeer) (PairingRequest, error) {
	iv := a.iv
	iv.mu.Lock()
	defer iv.mu.Unlock()
	if a.state == attemptCommitted {
		return a.replay(ctx, p)
	}
	if a.state != attemptOpen {
		return PairingRequest{}, ErrAttemptDone
	}
	now, ok := iv.now()
	if err := a.stillValid(now, ok); err != nil {
		a.end(attemptFailed)
		return PairingRequest{}, err
	}
	if a.outcome == nil {
		a.outcome = &PairingOutcome{OperationID: a.id, InvitationID: a.inv.id, IdentityKey: p.IdentityKey,
			Name: p.Name, TranscriptID: bytes.Clone(p.TranscriptID), At: now}
	} else if !p.matches(*a.outcome) {
		return PairingRequest{}, ErrOutcomeConflict
	}
	request, err := iv.store.Commit(ctx, *a.outcome)
	if err != nil {
		return PairingRequest{}, err
	}
	a.inv.consumed = true
	a.end(attemptCommitted)
	return request, nil
}

// stillValid refuses a late completion: after a clock rollback, cancel,
// replacement, expiry or the attempt deadline.
func (a *Attempt) stillValid(now time.Time, clockOK bool) error {
	switch {
	case !clockOK, !now.Before(a.inv.expiresAt):
		return ErrInvitationExpired
	case a.iv.current != a.inv:
		return ErrInvitationCancelled
	case !now.Before(a.deadline):
		return ErrAttemptDone
	}
	return nil
}

// replay returns the committed result for an identical repeat.
func (a *Attempt) replay(ctx context.Context, p PairedPeer) (PairingRequest, error) {
	if !p.matches(*a.outcome) {
		return PairingRequest{}, ErrOutcomeConflict
	}
	return a.iv.store.Commit(ctx, *a.outcome)
}

// String and GoString keep the code out of accidental formatting (%v, %+v, %#v).
func (i Invitation) String() string {
	return "invitation " + i.ID + " (code hidden), expires " + i.ExpiresAt.Format(time.RFC3339)
}

func (i Invitation) GoString() string { return "peer.Invitation{" + i.String() + "}" }
