package peer

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

// Short-code pairing state (#178). A confirmed pairing becomes a pairing
// request that the operator explicitly accepts or rejects; until then there is
// no contact.
//
// IdentityKey is the peer's verified stable identity-key fingerprint. It is
// provisional: how that key is generated and mapped to certificates is a later
// slice, and it is deliberately not the renewable TLS certificate fingerprint
// the probe uses. Names are display metadata only and never identify a peer.

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
	if o.OperationID == "" || o.InvitationID == "" || o.IdentityKey == "" || len(o.TranscriptID) == 0 || o.At.IsZero() {
		return errors.New("peer: pairing outcome is missing a required field")
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
