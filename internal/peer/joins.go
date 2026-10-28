package peer

import (
	"bytes"
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
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
//	awaiting_acceptance → contact | declined  (the inviter's decision, by Settle)
//	intent | exchanging → failed              (refused, or failed before verifying)
//	intent | exchanging → unverified          (RecoverJoins after a restart)
//
// outcome_unknown means our final confirmation may have reached the
// inviter; only an exactly matching receipt, at the start of a Settle, moves
// it on. contact and declined are final: they hold the inviter's
// authenticated decision, and DecisionConfirmedAt records, once, that the
// inviter took note of our acknowledgment of it.

// JoinState is where a join stands.
type JoinState string

const (
	JoinIntent             JoinState = "intent"
	JoinExchanging         JoinState = "exchanging"
	JoinOutcomeUnknown     JoinState = "outcome_unknown"
	JoinAwaitingAcceptance JoinState = "awaiting_acceptance"
	JoinFailed             JoinState = "failed"
	JoinUnverified         JoinState = "unverified"
	// JoinContact and JoinDeclined hold the inviter's authenticated decision;
	// both are final.
	JoinContact  JoinState = "contact"
	JoinDeclined JoinState = "declined"
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
	Destination        string // where the operator sent us; empty for a direct join
	InvitationID       string
	InviterOperationID string
	Peer               string // the authenticated inviter
	TranscriptID       []byte
	State              JoinState
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// DecisionConfirmedAt is when the inviter acknowledged our receipt of
	// its decision; until then a final join still settles again.
	DecisionConfirmedAt time.Time
}

// JoinReceipt is the inviter's word that it committed this join.
type JoinReceipt struct {
	InvitationID, InviterOperationID, Joiner string
	TranscriptID                             []byte
}

// JoinStore keeps joins durably. Each write checks and applies its
// transition atomically; an identical repeat returns the record unchanged.
type JoinStore interface {
	BeginJoin(ctx context.Context, s JoinStart) (JoinRecord, error)
	SetCorrelation(ctx context.Context, opID string, h InviterHello, at time.Time) (JoinRecord, error)
	RecordVerified(ctx context.Context, opID string, res ExchangeResult, at time.Time) (JoinRecord, error)
	RecordReceipt(ctx context.Context, opID string, r JoinReceipt, at time.Time) (JoinRecord, error)
	FailJoin(ctx context.Context, opID string, at time.Time) (JoinRecord, error)
	// RecoverJoins marks joins interrupted before verification unverified.
	// It runs once at startup, while no join can be live.
	RecoverJoins(ctx context.Context, at time.Time) (int, error)
	// RecordDecision records the inviter's decision on a join awaiting it;
	// an acceptance makes the contact in the same write.
	RecordDecision(ctx context.Context, opID string, decision PairingState, at time.Time) (JoinRecord, error)
	// RecordDecisionConfirmed records, once, that the inviter confirmed it.
	RecordDecisionConfirmed(ctx context.Context, opID string, decision PairingState, at time.Time) (JoinRecord, error)
	GetJoin(ctx context.Context, opID string) (JoinRecord, bool, error)
	Joins(ctx context.Context) ([]JoinRecord, error)
}

// Open reports a join that counts against MaxOpenJoins.
func (s JoinState) Open() bool {
	return slices.Contains([]JoinState{JoinIntent, JoinExchanging, JoinOutcomeUnknown, JoinAwaitingAcceptance}, s)
}

// decisionStates are where each decision leaves a join.
var decisionStates = map[PairingState]JoinState{PairingAccepted: JoinContact, PairingRejected: JoinDeclined}

// Decide records the inviter's decision on a join awaiting it. Repeating
// the decision changes nothing; a different one conflicts.
func Decide(r JoinRecord, decision PairingState, at time.Time) (JoinRecord, bool, error) {
	final, ok := decisionStates[decision]
	if !ok {
		return r, false, ErrInvalidJoin
	}
	if r.State == JoinAwaitingAcceptance {
		r.State, r.UpdatedAt = final, at
		return r, true, nil
	}
	return r, false, finalState(r.State, final)
}

// finalState answers a decision or confirmation on a join that may already
// hold one.
func finalState(current, want JoinState) error {
	if current == want {
		return nil
	}
	if current == JoinContact || current == JoinDeclined {
		return ErrJoinConflict
	}
	return ErrJoinState
}

// ConfirmDecision records, once, that the inviter durably took note of our
// acknowledgement of its decision.
func ConfirmDecision(r JoinRecord, decision PairingState, at time.Time) (JoinRecord, bool, error) {
	final, ok := decisionStates[decision]
	if !ok {
		return r, false, ErrInvalidJoin
	}
	if err := finalState(r.State, final); err != nil {
		return r, false, err
	}
	if !r.DecisionConfirmedAt.IsZero() {
		return r, false, nil
	}
	r.DecisionConfirmedAt, r.UpdatedAt = at, at
	return r, true, nil
}

// JoinerContact is the contact an accepted join makes.
func JoinerContact(r JoinRecord) Contact {
	return Contact{IdentityKey: r.Peer, Destination: r.Destination, EstablishedAt: r.UpdatedAt, Joined: true}
}

// JoinStart is a join's intent: our operation ID, our own fingerprint, the
// name we pair under and, for an operator's redemption, the destination.
type JoinStart struct {
	OperationID, Joiner, Name, Destination string
	At                                     time.Time
}

// JoinCounts is what admitting a new join depends on.
type JoinCounts struct {
	Total, Open int
}

// NewJoin is a join's first record, or why it can't be admitted.
func NewJoin(s JoinStart, c JoinCounts) (JoinRecord, error) {
	if err := s.validate(); err != nil {
		return JoinRecord{}, err
	}
	if c.Total >= MaxJoins {
		return JoinRecord{}, ErrJoinCapacity
	}
	if c.Open >= MaxOpenJoins {
		return JoinRecord{}, ErrTooManyOpenJoins
	}
	return JoinRecord{OperationID: s.OperationID, Joiner: s.Joiner, JoinerName: s.Name, Destination: s.Destination,
		State: JoinIntent, CreatedAt: s.At, UpdatedAt: s.At}, nil
}

func (s JoinStart) validate() error {
	if !validPairingID(s.OperationID) || !validPairingName(s.Name) {
		return ErrInvalidJoin
	}
	if ValidIdentityKey(s.Joiner) != nil || validDestination(s.Destination) != nil {
		return ErrInvalidJoin
	}
	return nil
}

// MaxDestinationBytes bounds where an operator may send a redemption.
const MaxDestinationBytes = 255

// ErrInvalidDestination: an oversized destination or one with control
// characters.
var ErrInvalidDestination = errors.New("peer: invalid destination")

// CheckDestination accepts an operator's destination: host:port, with a
// non-empty host and a decimal port from 1 to 65535.
func CheckDestination(d string) error {
	_, _, err := SplitDestination(d)
	return err
}

// SplitDestination checks a destination and returns its host and port, so
// every user of it reads the port the check accepted.
func SplitDestination(d string) (string, uint16, error) {
	if d == "" || validDestination(d) != nil {
		return "", 0, ErrInvalidDestination
	}
	host, port, err := net.SplitHostPort(d)
	if err != nil || host == "" {
		return "", 0, ErrInvalidDestination
	}
	n, err := parsePort(port)
	return host, n, err
}

// parsePort accepts digits only, no sign, from 1 to 65535.
func parsePort(port string) (uint16, error) {
	if !decimal(port) {
		return 0, ErrInvalidDestination
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0, ErrInvalidDestination
	}
	if n == 0 {
		return 0, ErrInvalidDestination
	}
	return uint16(n), nil
}

// decimal reports a non-empty string of ASCII digits.
func decimal(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
}

// validDestination accepts the empty destination of a direct join; an
// operator's redemption must also name one, see JoinRequest.
func validDestination(d string) error {
	if len(d) > MaxDestinationBytes || strings.ContainsFunc(d, unicode.IsControl) {
		return ErrInvalidDestination
	}
	return nil
}

func validPairingID(id string) bool {
	return id != "" && len(id) <= MaxPairingIDBytes
}

// ReplayJoin answers BeginJoin for a known operation.
func ReplayJoin(r JoinRecord, s JoinStart) (JoinRecord, error) {
	same := []bool{r.Joiner == s.Joiner, r.JoinerName == s.Name, r.Destination == s.Destination}
	if slices.Contains(same, false) {
		return JoinRecord{}, ErrJoinConflict
	}
	return r, nil
}

// Each transition returns the new record and whether it changed, or why it
// can't apply. Stores call them on the record read in their transaction.

// Correlate records the inviter's hello.
func Correlate(r JoinRecord, h InviterHello, at time.Time) (JoinRecord, bool, error) {
	if !validPairingID(h.InvitationID) || !validPairingID(h.OperationID) {
		return r, false, ErrInvalidJoin
	}
	if r.State == JoinIntent {
		r.InvitationID, r.InviterOperationID, r.State, r.UpdatedAt = h.InvitationID, h.OperationID, JoinExchanging, at
		return r, true, nil
	}
	return r, false, sameOnce(r.InviterOperationID != "", r.InvitationID == h.InvitationID && r.InviterOperationID == h.OperationID)
}

// Verify records the authenticated inviter and transcript, before our final
// confirmation can leave.
func Verify(r JoinRecord, res ExchangeResult, at time.Time) (JoinRecord, bool, error) {
	if ValidIdentityKey(res.Peer) != nil || !validTranscriptID(res.TranscriptID) {
		return r, false, ErrInvalidJoin
	}
	if r.State == JoinExchanging {
		r.Peer, r.TranscriptID, r.State, r.UpdatedAt = res.Peer, bytes.Clone(res.TranscriptID), JoinOutcomeUnknown, at
		return r, true, nil
	}
	return r, false, sameOnce(r.Peer != "", r.Peer == res.Peer && bytes.Equal(r.TranscriptID, res.TranscriptID))
}

func validTranscriptID(id []byte) bool {
	return len(id) > 0 && len(id) <= MaxTranscriptIDBytes
}

// ApplyReceipt accepts a receipt only if every field equals what we recorded.
func ApplyReceipt(r JoinRecord, receipt JoinReceipt, at time.Time) (JoinRecord, bool, error) {
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
	*MemContacts
	mu    sync.Mutex
	joins map[string]JoinRecord
}

func NewMemJoinStore() *MemJoinStore {
	return NewMemJoinStoreWith(NewMemContacts())
}

// NewMemJoinStoreWith shares contacts with a pairing store.
func NewMemJoinStoreWith(contacts *MemContacts) *MemJoinStore {
	return &MemJoinStore{MemContacts: contacts, joins: map[string]JoinRecord{}}
}

func (s *MemJoinStore) RecordDecision(_ context.Context, opID string, decision PairingState, at time.Time) (JoinRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.joins[opID]
	if !ok {
		return JoinRecord{}, ErrNoJoin
	}
	next, changed, err := Decide(r, decision, at)
	if err != nil || !changed {
		return cloneJoin(next), err
	}
	commit := func() { s.joins[opID] = next }
	if next.State != JoinContact {
		commit()
		return cloneJoin(next), nil
	}
	if err := s.apply(JoinerContact(next), commit); err != nil {
		return cloneJoin(r), err
	}
	return cloneJoin(next), nil
}

func (s *MemJoinStore) RecordDecisionConfirmed(_ context.Context, opID string, decision PairingState, at time.Time) (JoinRecord, error) {
	return s.update(opID, func(r JoinRecord) (JoinRecord, bool, error) { return ConfirmDecision(r, decision, at) })
}

func cloneJoin(r JoinRecord) JoinRecord {
	r.TranscriptID = bytes.Clone(r.TranscriptID)
	return r
}

func (s *MemJoinStore) BeginJoin(_ context.Context, start JoinStart) (JoinRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.joins[start.OperationID]; ok {
		r, err := ReplayJoin(r, start)
		return cloneJoin(r), err
	}
	r, err := NewJoin(start, s.counts())
	if err != nil {
		return JoinRecord{}, err
	}
	s.joins[start.OperationID] = r
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

func (s *MemJoinStore) SetCorrelation(_ context.Context, opID string, h InviterHello, at time.Time) (JoinRecord, error) {
	return s.update(opID, func(r JoinRecord) (JoinRecord, bool, error) { return Correlate(r, h, at) })
}

func (s *MemJoinStore) RecordVerified(_ context.Context, opID string, res ExchangeResult, at time.Time) (JoinRecord, error) {
	return s.update(opID, func(r JoinRecord) (JoinRecord, bool, error) { return Verify(r, res, at) })
}

func (s *MemJoinStore) RecordReceipt(_ context.Context, opID string, receipt JoinReceipt, at time.Time) (JoinRecord, error) {
	return s.update(opID, func(r JoinRecord) (JoinRecord, bool, error) { return ApplyReceipt(r, receipt, at) })
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
