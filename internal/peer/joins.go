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

// Joins (#178) are this workspace's attempts to redeem another workspace's
// code. The joiner's consent is the redemption itself, so a join that the
// inviter durably committed awaits only the inviter's acceptance.
//
// A join records, before anything could let the inviter commit, everything
// needed to reconcile it later: the authenticated inviter, both operation
// IDs, the invitation and the exchange's transcript ID. Never the code or
// any exchange secret. States only move forward:
//
//	intent → exchanging → outcome_unknown → awaiting_acceptance
//	intent | exchanging → failed          (refused, or failed before verifying)
//	intent | exchanging → unverified      (RecoverJoins after a restart)
//
// outcome_unknown means our final confirmation may have reached the
// inviter; only an exactly matching receipt settles it. Acceptance and
// rejection arrive in a later slice.

// JoinState is where a join stands.
type JoinState string

const (
	JoinIntent             JoinState = "intent"
	JoinExchanging         JoinState = "exchanging"
	JoinOutcomeUnknown     JoinState = "outcome_unknown"
	JoinAwaitingAcceptance JoinState = "awaiting_acceptance"
	JoinFailed             JoinState = "failed"
	JoinUnverified         JoinState = "unverified"
)

// Join bounds: open joins (not failed or unverified) and all joins.
const (
	MaxOpenJoins = 64
	MaxJoins     = 1024
)

var (
	ErrNoJoin           = errors.New("peer: no such join")
	ErrJoinConflict     = errors.New("peer: join already holds different values")
	ErrJoinState        = errors.New("peer: join is not in a state for that")
	ErrInvalidJoin      = errors.New("peer: invalid join field")
	ErrJoinCapacity     = errors.New("peer: join capacity reached")
	ErrTooManyOpenJoins = errors.New("peer: too many open joins")
)

// JoinRecord is one join. Every field but State and UpdatedAt is written
// once.
type JoinRecord struct {
	OperationID        string // ours
	Joiner             string // our own fingerprint
	JoinerName         string
	InvitationID       string
	InviterOperationID string
	Peer               string // the authenticated inviter
	TranscriptID       []byte
	State              JoinState
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// JoinReceipt is the inviter's word that it committed this join.
type JoinReceipt struct {
	InvitationID, InviterOperationID, Joiner string
	TranscriptID                             []byte
}

// JoinStore keeps joins durably. Each write checks and applies its
// transition atomically; an identical repeat returns the record unchanged.
type JoinStore interface {
	BeginJoin(ctx context.Context, opID, joiner, name string, at time.Time) (JoinRecord, error)
	SetCorrelation(ctx context.Context, opID, invitationID, inviterOpID string, at time.Time) (JoinRecord, error)
	RecordVerified(ctx context.Context, opID, peer string, transcriptID []byte, at time.Time) (JoinRecord, error)
	RecordReceipt(ctx context.Context, opID string, r JoinReceipt, at time.Time) (JoinRecord, error)
	FailJoin(ctx context.Context, opID string, at time.Time) (JoinRecord, error)
	// RecoverJoins marks joins interrupted before verification unverified.
	// It runs once at startup, while no join can be live.
	RecoverJoins(ctx context.Context, at time.Time) (int, error)
	GetJoin(ctx context.Context, opID string) (JoinRecord, bool, error)
	Joins(ctx context.Context) ([]JoinRecord, error)
}

// Open reports a join that counts against MaxOpenJoins.
func (s JoinState) Open() bool {
	return s != JoinFailed && s != JoinUnverified
}

// JoinCounts is what admitting a new join depends on.
type JoinCounts struct {
	Total, Open int
}

// NewJoin is a join's first record, or why it can't be admitted.
func NewJoin(opID, joiner, name string, at time.Time, c JoinCounts) (JoinRecord, error) {
	if err := validJoinStart(opID, joiner, name); err != nil {
		return JoinRecord{}, err
	}
	if c.Total >= MaxJoins {
		return JoinRecord{}, ErrJoinCapacity
	}
	if c.Open >= MaxOpenJoins {
		return JoinRecord{}, ErrTooManyOpenJoins
	}
	return JoinRecord{OperationID: opID, Joiner: joiner, JoinerName: name, State: JoinIntent, CreatedAt: at, UpdatedAt: at}, nil
}

func validJoinStart(opID, joiner, name string) error {
	if !validPairingID(opID) || !validPairingName(name) {
		return ErrInvalidJoin
	}
	if ValidIdentityKey(joiner) != nil {
		return ErrInvalidJoin
	}
	return nil
}

func validPairingID(id string) bool {
	return id != "" && len(id) <= MaxPairingIDBytes
}

// ReplayJoin answers BeginJoin for a known operation.
func ReplayJoin(r JoinRecord, joiner, name string) (JoinRecord, error) {
	if r.Joiner != joiner || r.JoinerName != name {
		return JoinRecord{}, ErrJoinConflict
	}
	return r, nil
}

// Each transition returns the new record and whether it changed, or why it
// can't apply. Stores call them on the record read in their transaction.

// Correlate records the inviter's hello.
func Correlate(r JoinRecord, invitationID, inviterOpID string, at time.Time) (JoinRecord, bool, error) {
	if !validPairingID(invitationID) || !validPairingID(inviterOpID) {
		return r, false, ErrInvalidJoin
	}
	if r.State == JoinIntent {
		r.InvitationID, r.InviterOperationID, r.State, r.UpdatedAt = invitationID, inviterOpID, JoinExchanging, at
		return r, true, nil
	}
	return r, false, sameOnce(r.InviterOperationID != "", r.InvitationID == invitationID && r.InviterOperationID == inviterOpID)
}

// Verify records the authenticated inviter and transcript, before our final
// confirmation can leave.
func Verify(r JoinRecord, peer string, transcriptID []byte, at time.Time) (JoinRecord, bool, error) {
	if ValidIdentityKey(peer) != nil || !validTranscriptID(transcriptID) {
		return r, false, ErrInvalidJoin
	}
	if r.State == JoinExchanging {
		r.Peer, r.TranscriptID, r.State, r.UpdatedAt = peer, bytes.Clone(transcriptID), JoinOutcomeUnknown, at
		return r, true, nil
	}
	return r, false, sameOnce(r.Peer != "", r.Peer == peer && bytes.Equal(r.TranscriptID, transcriptID))
}

func validTranscriptID(id []byte) bool {
	return len(id) > 0 && len(id) <= MaxTranscriptIDBytes
}

// Settle accepts a receipt only if every field equals what we recorded.
func Settle(r JoinRecord, receipt JoinReceipt, at time.Time) (JoinRecord, bool, error) {
	if r.Peer == "" {
		return r, false, ErrJoinState
	}
	if !receiptMatches(r, receipt) {
		return r, false, ErrJoinConflict
	}
	if r.State == JoinOutcomeUnknown {
		r.State, r.UpdatedAt = JoinAwaitingAcceptance, at
		return r, true, nil
	}
	return r, false, sameOnce(r.State == JoinAwaitingAcceptance, true)
}

func receiptMatches(r JoinRecord, receipt JoinReceipt) bool {
	same := []bool{
		receipt.InvitationID == r.InvitationID,
		receipt.InviterOperationID == r.InviterOperationID,
		receipt.Joiner == r.Joiner,
		bytes.Equal(receipt.TranscriptID, r.TranscriptID),
	}
	return !slices.Contains(same, false)
}

// Fail ends a join that never verified the inviter.
func Fail(r JoinRecord, at time.Time) (JoinRecord, bool, error) {
	switch r.State {
	case JoinIntent, JoinExchanging:
		r.State, r.UpdatedAt = JoinFailed, at
		return r, true, nil
	case JoinFailed:
		return r, false, nil
	}
	return r, false, ErrJoinState
}

// Unverify is RecoverJoins' transition for one record.
func Unverify(r JoinRecord, at time.Time) (JoinRecord, bool) {
	if r.State != JoinIntent && r.State != JoinExchanging {
		return r, false
	}
	r.State, r.UpdatedAt = JoinUnverified, at
	return r, true
}

// sameOnce answers a repeat of a write-once step: set and equal is a no-op,
// set and different conflicts, never set means the state is wrong.
func sameOnce(set, equal bool) error {
	if !set {
		return ErrJoinState
	}
	if !equal {
		return ErrJoinConflict
	}
	return nil
}

// MatchReceipt reports whether the inviter's committed outcome is exactly
// the one a receipt names. It only reads: it never begins an attempt or
// writes an outcome.
func MatchReceipt(ctx context.Context, s PairingStore, r JoinReceipt) (bool, error) {
	o, found, err := s.Outcome(ctx, r.InviterOperationID)
	if err != nil || !found {
		return false, err
	}
	same := []bool{o.InvitationID == r.InvitationID, o.IdentityKey == r.Joiner, bytes.Equal(o.TranscriptID, r.TranscriptID)}
	return !slices.Contains(same, false), nil
}

// MemJoinStore is an in-memory JoinStore for tests.
type MemJoinStore struct {
	mu    sync.Mutex
	joins map[string]JoinRecord
}

func NewMemJoinStore() *MemJoinStore {
	return &MemJoinStore{joins: map[string]JoinRecord{}}
}

func cloneJoin(r JoinRecord) JoinRecord {
	r.TranscriptID = bytes.Clone(r.TranscriptID)
	return r
}

func (s *MemJoinStore) BeginJoin(_ context.Context, opID, joiner, name string, at time.Time) (JoinRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.joins[opID]; ok {
		r, err := ReplayJoin(r, joiner, name)
		return cloneJoin(r), err
	}
	r, err := NewJoin(opID, joiner, name, at, s.counts())
	if err != nil {
		return JoinRecord{}, err
	}
	s.joins[opID] = r
	return cloneJoin(r), nil
}

func (s *MemJoinStore) counts() JoinCounts {
	c := JoinCounts{Total: len(s.joins)}
	for _, r := range s.joins {
		if r.State.Open() {
			c.Open++
		}
	}
	return c
}

// update applies a transition to a known join.
func (s *MemJoinStore) update(opID string, step func(JoinRecord) (JoinRecord, bool, error)) (JoinRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.joins[opID]
	if !ok {
		return JoinRecord{}, ErrNoJoin
	}
	next, changed, err := step(r)
	if err != nil {
		return JoinRecord{}, err
	}
	if changed {
		s.joins[opID] = next
	}
	return cloneJoin(next), nil
}

func (s *MemJoinStore) SetCorrelation(_ context.Context, opID, invitationID, inviterOpID string, at time.Time) (JoinRecord, error) {
	return s.update(opID, func(r JoinRecord) (JoinRecord, bool, error) { return Correlate(r, invitationID, inviterOpID, at) })
}

func (s *MemJoinStore) RecordVerified(_ context.Context, opID, peer string, transcriptID []byte, at time.Time) (JoinRecord, error) {
	return s.update(opID, func(r JoinRecord) (JoinRecord, bool, error) { return Verify(r, peer, transcriptID, at) })
}

func (s *MemJoinStore) RecordReceipt(_ context.Context, opID string, receipt JoinReceipt, at time.Time) (JoinRecord, error) {
	return s.update(opID, func(r JoinRecord) (JoinRecord, bool, error) { return Settle(r, receipt, at) })
}

func (s *MemJoinStore) FailJoin(_ context.Context, opID string, at time.Time) (JoinRecord, error) {
	return s.update(opID, func(r JoinRecord) (JoinRecord, bool, error) { return Fail(r, at) })
}

func (s *MemJoinStore) RecoverJoins(_ context.Context, at time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, r := range s.joins {
		if next, changed := Unverify(r, at); changed {
			s.joins[id] = next
			n++
		}
	}
	return n, nil
}

func (s *MemJoinStore) GetJoin(_ context.Context, opID string) (JoinRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.joins[opID]
	return cloneJoin(r), ok, nil
}

func (s *MemJoinStore) Joins(context.Context) ([]JoinRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JoinRecord, 0, len(s.joins))
	for _, r := range s.joins {
		out = append(out, cloneJoin(r))
	}
	slices.SortFunc(out, func(a, b JoinRecord) int { return strings.Compare(a.OperationID, b.OperationID) })
	return out, nil
}
