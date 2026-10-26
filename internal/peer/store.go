package peer

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Store holds contact state. The gate calls it; a request is identified by
// the peer's certificate fingerprint and its request ID, so a retry of the
// same request repeats its outcome and never makes a second one.
//
// Inbound requests (a peer asking us) and outbound requests (us asking a
// peer) are separate. Only the local operator decides an inbound request,
// through Accept or Ignore. A peer's contact_accept or contact_ignore on
// the wire can only settle an outbound request we made to that peer, through
// Settle; it can never approve the peer's own inbound request.
type Store interface {
	// SaveRequest records an inbound contact request once. A repeat with the
	// same content returns the existing outcome with Existing set; the same
	// ID with different content is ErrConflict.
	SaveRequest(ctx context.Context, req Request) (Outcome, error)
	// Accept and Ignore are the local operator's decisions on an inbound
	// request; both are idempotent, and a decision never flips once made.
	Accept(ctx context.Context, ref ContactRef) (Outcome, error)
	Ignore(ctx context.Context, ref ContactRef) (Outcome, error)
	// Get reads an inbound request's outcome; ok is false when it was never
	// stored.
	Get(ctx context.Context, ref ContactRef) (Outcome, bool, error)
	// SaveOutbound records a request we sent to the peer, pending until the
	// peer settles it.
	SaveOutbound(ctx context.Context, ref ContactRef) (Outcome, error)
	// Settle applies the peer's answer to our outbound request: KindAccept
	// or KindIgnore. ErrUnknown when we never asked.
	Settle(ctx context.Context, ref ContactRef, answer Kind) (Outcome, error)
	// Pending counts the inbound requests still waiting for a decision, in
	// total and for one peer, so the gate can refuse past a bound.
	Pending(ctx context.Context, fingerprint string) (total, forPeer int, err error)
}

// ContactRef names one request: the peer's certificate fingerprint and the
// request ID it chose.
type ContactRef struct {
	Fingerprint string
	RequestID   string
}

// Request is a contact request as the gate received it.
type Request struct {
	Fingerprint string
	Cert        []byte // the peer's leaf certificate, DER
	Record      Record
	ReceivedAt  time.Time
}

// Ref is the request's reference.
func (r Request) Ref() ContactRef {
	return ContactRef{Fingerprint: r.Fingerprint, RequestID: r.Record.RequestID}
}

// Outcome is a request's current state.
type Outcome struct {
	Status   string // pending, accepted or ignored
	Existing bool   // the request was already stored
}

var (
	ErrConflict = errors.New("a different request already uses this id")
	ErrUnknown  = errors.New("no such request")
	ErrDecided  = errors.New("the request was already decided")
)

// MemStore is the probe's in-memory store. It proves the gate's rules; the
// broker's SQLite store replaces it in the integration slice.
type MemStore struct {
	mu   sync.Mutex
	reqs map[storeKey]*stored
}

type storeKey struct {
	ref      ContactRef
	outbound bool
}

type stored struct {
	req    Request
	status string
}

func NewMemStore() *MemStore { return &MemStore{reqs: map[storeKey]*stored{}} }

func (s *MemStore) SaveRequest(_ context.Context, req Request) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := storeKey{ref: req.Ref()}
	if have, ok := s.reqs[key]; ok {
		if have.req.Record != req.Record {
			return Outcome{}, ErrConflict
		}
		return Outcome{Status: have.status, Existing: true}, nil
	}
	s.reqs[key] = &stored{req: req, status: StatusPending}
	return Outcome{Status: StatusPending}, nil
}

func (s *MemStore) Accept(_ context.Context, ref ContactRef) (Outcome, error) {
	return s.settle(storeKey{ref: ref}, StatusAccepted)
}

func (s *MemStore) Ignore(_ context.Context, ref ContactRef) (Outcome, error) {
	return s.settle(storeKey{ref: ref}, StatusIgnored)
}

func (s *MemStore) SaveOutbound(_ context.Context, ref ContactRef) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := storeKey{ref: ref, outbound: true}
	if have, ok := s.reqs[key]; ok {
		return Outcome{Status: have.status, Existing: true}, nil
	}
	s.reqs[key] = &stored{status: StatusPending}
	return Outcome{Status: StatusPending}, nil
}

func (s *MemStore) Settle(_ context.Context, ref ContactRef, answer Kind) (Outcome, error) {
	status, ok := settlement[answer]
	if !ok {
		return Outcome{}, errors.New("a settlement is contact_accept or contact_ignore")
	}
	return s.settle(storeKey{ref: ref, outbound: true}, status)
}

// settlement is the status a peer's answer gives our outbound request.
var settlement = map[Kind]string{KindAccept: StatusAccepted, KindIgnore: StatusIgnored}

// settle moves a pending request to its decision, once.
func (s *MemStore) settle(key storeKey, status string) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	have, ok := s.reqs[key]
	switch {
	case !ok:
		return Outcome{}, ErrUnknown
	case have.status == status:
		return Outcome{Status: status, Existing: true}, nil
	case have.status != StatusPending:
		return Outcome{Status: have.status, Existing: true}, ErrDecided
	}
	have.status = status
	return Outcome{Status: status}, nil
}

func (s *MemStore) Get(_ context.Context, ref ContactRef) (Outcome, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	have, ok := s.reqs[storeKey{ref: ref}]
	if !ok {
		return Outcome{}, false, nil
	}
	return Outcome{Status: have.status, Existing: true}, true, nil
}

func (s *MemStore) Pending(_ context.Context, fingerprint string) (total, forPeer int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, have := range s.reqs {
		if have.status != StatusPending || key.outbound {
			continue
		}
		total++
		if key.ref.Fingerprint == fingerprint {
			forPeer++
		}
	}
	return total, forPeer, nil
}

// Snapshot lists every stored request and its status, keyed
// "in/<fingerprint>/<id>" or "out/<fingerprint>/<id>", for tests that must
// observe state directly rather than infer it from replies.
func (s *MemStore) Snapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.reqs))
	for key, have := range s.reqs {
		dir := "in"
		if key.outbound {
			dir = "out"
		}
		out[dir+"/"+key.ref.Fingerprint+"/"+key.ref.RequestID] = have.status
	}
	return out
}
