package peer

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Short-code pairing state (#178). A confirmed pairing becomes a pairing
// request that the operator explicitly accepts or rejects; until then there is
// no contact.
//
// IdentityKey is the peer's pairtask SPKI fingerprint (see WorkspaceIdentity),
// never a certificate hash, so renewing a certificate keeps the identity.
// Names are display metadata only and never identify a peer.
//
// Storage is bounded and nothing is evicted: a full store refuses new
// records visibly, while an exact retry of a committed outcome still
// succeeds. Rows from before these bounds are kept as they are.

// PairingState is where a pairing request stands.
type PairingState string

const (
	PairingPending  PairingState = "pending_acceptance"
	PairingAccepted PairingState = "accepted"
	PairingRejected PairingState = "rejected"
)

var (
	// ErrOutcomeConflict means an operation or invitation already has a
	// different committed outcome; nothing was written.
	ErrOutcomeConflict = errors.New("peer: pairing outcome conflicts with a committed one")
	// ErrNotPending means the request was already decided the other way.
	ErrNotPending = errors.New("peer: pairing request is not pending")
	// ErrNoPairingRequest means no request exists for that identity.
	ErrNoPairingRequest = errors.New("peer: no pairing request for that identity")
	// ErrInvalidPairingName: a name longer than MaxPairingNameBytes, not
	// UTF-8, or with control or non-printing characters.
	ErrInvalidPairingName = errors.New("peer: invalid pairing name")
	// ErrInvalidPairingOutcome: an operation, invitation or transcript ID
	// beyond its byte bound.
	ErrInvalidPairingOutcome = errors.New("peer: pairing outcome field too long")
	// ErrOutcomeCapacity, ErrIdentityCapacity and ErrTooManyPending refuse a
	// new record to a full store.
	ErrOutcomeCapacity  = errors.New("peer: pairing outcome capacity reached")
	ErrIdentityCapacity = errors.New("peer: pairing identity capacity reached")
	ErrTooManyPending   = errors.New("peer: too many pairing requests awaiting a decision")
)

// Storage bounds. Pending requests fit one listing, so every request awaiting
// a decision can be shown.
const (
	MaxPendingPairingRequests = 64
	MaxPairingIdentities      = 1024
	MaxPairingOutcomes        = 4096
	MaxPairingNameBytes       = 64
	MaxPairingIDBytes         = 128
	MaxTranscriptIDBytes      = 64 // SHA-512 at most
)

// PairingOutcome is one successful pairing operation, kept durably so a repeat
// or a restart can find what was committed. Every field is immutable once
// committed.
type PairingOutcome struct {
	OperationID  string
	InvitationID string
	IdentityKey  string
	Name         string
	TranscriptID []byte
	At           time.Time
}

// PairingRequest is the current decision state for one identity.
type PairingRequest struct {
	IdentityKey   string
	Name          string
	State         PairingState
	FirstPairedAt time.Time
}

// PairingStore keeps pairing outcomes and requests durably.
type PairingStore interface {
	// Commit writes the outcome and its request in one transaction:
	//   - the same operation with identical content returns the request again;
	//   - the same operation or invitation with anything different is
	//     ErrOutcomeConflict and writes nothing;
	//   - a new outcome for a known identity keeps the request's decision, so
	//     a rejected request is never revived;
	//   - otherwise a pending request is created.
	Commit(ctx context.Context, o PairingOutcome) (PairingRequest, error)
	// Outcome looks up a committed operation, across restarts.
	Outcome(ctx context.Context, operationID string) (PairingOutcome, bool, error)
	// Accept and Reject decide a pending request. Repeating the same decision
	// returns the request; the opposite decision is ErrNotPending.
	Accept(ctx context.Context, identityKey string) (PairingRequest, error)
	Reject(ctx context.Context, identityKey string) (PairingRequest, error)
	Request(ctx context.Context, identityKey string) (PairingRequest, bool, error)
	Requests(ctx context.Context) ([]PairingRequest, error)
}

// ValidOutcome reports a missing required field, so stores refuse it alike.
func ValidOutcome(o PairingOutcome) error {
	present := []bool{o.OperationID != "", o.InvitationID != "", o.IdentityKey != "", len(o.TranscriptID) > 0, !o.At.IsZero()}
	if slices.Contains(present, false) {
		return errors.New("peer: pairing outcome is missing a required field")
	}
	return nil
}

// ValidNewOutcome checks what a new outcome may hold. It runs after the
// exact-retry and conflict checks, so committed rows are never re-judged.
func ValidNewOutcome(o PairingOutcome) error {
	if err := ValidIdentityKey(o.IdentityKey); err != nil {
		return err
	}
	if !validPairingName(o.Name) {
		return ErrInvalidPairingName
	}
	sizes := []bool{len(o.OperationID) <= MaxPairingIDBytes, len(o.InvitationID) <= MaxPairingIDBytes, len(o.TranscriptID) <= MaxTranscriptIDBytes}
	if slices.Contains(sizes, false) {
		return ErrInvalidPairingOutcome
	}
	return nil
}

// CheckPairingName accepts a display name of at most MaxPairingNameBytes
// printable UTF-8 bytes.
func CheckPairingName(name string) error {
	if !validPairingName(name) {
		return ErrInvalidPairingName
	}
	return nil
}

func validPairingName(name string) bool {
	if len(name) > MaxPairingNameBytes || !utf8.ValidString(name) {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) })
}

// PairingCounts is what admitting a new outcome depends on, read atomically
// with the commit.
type PairingCounts struct {
	Outcomes, Identities, Pending int
	KnownIdentity                 bool
}

// AdmitOutcome refuses a new outcome to a full store. A known identity adds
// no request, so only the outcome bound applies to it.
func AdmitOutcome(c PairingCounts) error {
	switch {
	case c.Outcomes >= MaxPairingOutcomes:
		return ErrOutcomeCapacity
	case c.KnownIdentity:
		return nil
	case c.Identities >= MaxPairingIdentities:
		return ErrIdentityCapacity
	case c.Pending >= MaxPendingPairingRequests:
		return ErrTooManyPending
	}
	return nil
}

// SameOutcome compares every immutable field of two outcomes.
func SameOutcome(a, b PairingOutcome) bool {
	return a.OperationID == b.OperationID && a.InvitationID == b.InvitationID &&
		a.IdentityKey == b.IdentityKey && a.Name == b.Name &&
		bytes.Equal(a.TranscriptID, b.TranscriptID) && a.At.Equal(b.At)
}

// DecidePairing applies an operator decision to a request's state.
func DecidePairing(current, decision PairingState) (PairingState, error) {
	switch current {
	case PairingPending, decision:
		return decision, nil
	}
	return current, ErrNotPending
}

// MemPairingStore is an in-memory PairingStore for tests and the probe.
type MemPairingStore struct {
	mu           sync.Mutex
	requests     map[string]PairingRequest
	outcomes     map[string]PairingOutcome
	byInvitation map[string]string
}

// NewMemPairingStore returns an empty store.
func NewMemPairingStore() *MemPairingStore {
	return &MemPairingStore{
		requests:     map[string]PairingRequest{},
		outcomes:     map[string]PairingOutcome{},
		byInvitation: map[string]string{},
	}
}

func cloneOutcome(o PairingOutcome) PairingOutcome {
	o.TranscriptID = bytes.Clone(o.TranscriptID)
	return o
}

func (s *MemPairingStore) Commit(_ context.Context, o PairingOutcome) (PairingRequest, error) {
	if err := ValidOutcome(o); err != nil {
		return PairingRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if known, ok := s.outcomes[o.OperationID]; ok {
		if !SameOutcome(known, o) {
			return PairingRequest{}, ErrOutcomeConflict
		}
		return s.requests[known.IdentityKey], nil
	}
	if _, used := s.byInvitation[o.InvitationID]; used {
		return PairingRequest{}, ErrOutcomeConflict
	}
	if err := ValidNewOutcome(o); err != nil {
		return PairingRequest{}, err
	}
	if err := AdmitOutcome(s.counts(o.IdentityKey)); err != nil {
		return PairingRequest{}, err
	}
	request, ok := s.requests[o.IdentityKey]
	if !ok {
		request = PairingRequest{IdentityKey: o.IdentityKey, State: PairingPending, FirstPairedAt: o.At}
	}
	request.Name = o.Name
	s.requests[o.IdentityKey] = request
	s.outcomes[o.OperationID] = cloneOutcome(o)
	s.byInvitation[o.InvitationID] = o.OperationID
	return request, nil
}

func (s *MemPairingStore) counts(identityKey string) PairingCounts {
	_, known := s.requests[identityKey]
	c := PairingCounts{Outcomes: len(s.outcomes), Identities: len(s.requests), KnownIdentity: known}
	for _, r := range s.requests {
		if r.State == PairingPending {
			c.Pending++
		}
	}
	return c
}

func (s *MemPairingStore) Outcome(_ context.Context, operationID string) (PairingOutcome, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.outcomes[operationID]
	return cloneOutcome(o), ok, nil
}

func (s *MemPairingStore) decide(identityKey string, decision PairingState) (PairingRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[identityKey]
	if !ok {
		return PairingRequest{}, ErrNoPairingRequest
	}
	state, err := DecidePairing(request.State, decision)
	if err != nil {
		return request, err
	}
	request.State = state
	s.requests[identityKey] = request
	return request, nil
}

func (s *MemPairingStore) Accept(_ context.Context, identityKey string) (PairingRequest, error) {
	return s.decide(identityKey, PairingAccepted)
}

func (s *MemPairingStore) Reject(_ context.Context, identityKey string) (PairingRequest, error) {
	return s.decide(identityKey, PairingRejected)
}

func (s *MemPairingStore) Request(_ context.Context, identityKey string) (PairingRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[identityKey]
	return request, ok, nil
}

func (s *MemPairingStore) Requests(context.Context) ([]PairingRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PairingRequest, 0, len(s.requests))
	for _, request := range s.requests {
		out = append(out, request)
	}
	slices.SortFunc(out, func(a, b PairingRequest) int { return strings.Compare(a.IdentityKey, b.IdentityKey) })
	return out, nil
}
