package forge

import (
	"context"
	"strconv"
	"strings"
	"sync"
)

// Fake is a scriptable forge for tests: its own identifiers (repository
// fake.example/space/repo, changes like CR-7, reviews fk-N) keep tests from
// leaning on one forge's shapes. It records every publication attempt, so a
// test can count writes.
type Fake struct {
	mu        sync.Mutex
	name      string
	identity  string
	caps      Caps
	revisions map[string]Revisions
	changes   map[string][]FileChange
	reviews   map[string][]fakeReview
	attempts  []Submission
	failures  map[string][]error
	landThen  error // Publish records the review, then fails with this
	seq       int
}

type fakeReview struct {
	receipt Receipt
	marker  string
}

// Fake operations, for Fail.
const (
	OpIdentity = "identity"
	OpResolve  = "resolve"
	OpChanges  = "changes"
	OpPublish  = "publish"
	OpFind     = "find"
)

// NewFake is a forge named "fake" that can do everything, publishing as
// "fake-reviewer".
func NewFake() *Fake {
	return &Fake{name: "fake", identity: "fake-reviewer",
		caps:      Caps{FormalApprove: true, FormalRequestChanges: true, LineComments: true, Reconcile: true},
		revisions: map[string]Revisions{}, changes: map[string][]FileChange{}, reviews: map[string][]fakeReview{},
		failures: map[string][]error{}}
}

func key(t Target) string { return t.Repository + "\x00" + t.Change }

// SetName, SetIdentity and SetCapabilities change what the fake reports.
func (f *Fake) SetName(name string)   { f.locked(func() { f.name = name }) }
func (f *Fake) SetIdentity(id string) { f.locked(func() { f.identity = id }) }
func (f *Fake) SetCapabilities(c Caps) {
	f.locked(func() { f.caps = c })
}

// SetRevisions is the change's current base and head.
func (f *Fake) SetRevisions(t Target, r Revisions) { f.locked(func() { f.revisions[key(t)] = r }) }

// SetChanges is what the change modified at its current revisions. What it
// modified at earlier revisions stays available, as a forge's history does.
func (f *Fake) SetChanges(t Target, files []FileChange) {
	f.locked(func() { f.changes[at(t, f.revisions[key(t)])] = files })
}

// SetChangesAt is what the change modified between revisions r.
func (f *Fake) SetChangesAt(t Target, r Revisions, files []FileChange) {
	f.locked(func() { f.changes[at(t, r)] = files })
}

func at(t Target, r Revisions) string { return key(t) + "\x00" + r.Base + "\x00" + r.Head }

// Fail makes the next call of op fail with err; calls queue up in order.
func (f *Fake) Fail(op string, err error) {
	f.locked(func() { f.failures[op] = append(f.failures[op], err) })
}

// LandThenFail makes the next Publish record the review and then return
// err, as a lost response would.
func (f *Fake) LandThenFail(err error) { f.locked(func() { f.landThen = err }) }

// AddReview puts a review on the change as someone else might have, marker
// included, to test that a copied marker is never taken as ours.
func (f *Fake) AddReview(t Target, r Receipt, marker string) {
	f.locked(func() { f.reviews[key(t)] = append(f.reviews[key(t)], fakeReview{r, marker}) })
}

// Attempts is every Publish call, failed ones included.
func (f *Fake) Attempts() []Submission {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Submission(nil), f.attempts...)
}

// Reviews is what the change holds now.
func (f *Fake) Reviews(t Target) []Receipt {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Receipt
	for _, r := range f.reviews[key(t)] {
		out = append(out, r.receipt)
	}
	return out
}

func (f *Fake) locked(do func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	do()
}

// failure takes the next scripted failure of op; the caller holds the lock.
func (f *Fake) failure(op string) error {
	queue := f.failures[op]
	if len(queue) == 0 {
		return nil
	}
	f.failures[op] = queue[1:]
	return queue[0]
}

func (f *Fake) Name() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.name
}

func (f *Fake) Capabilities() Caps {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caps
}

func (f *Fake) Identity(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(OpIdentity); err != nil {
		return "", err
	}
	return f.identity, nil
}

func (f *Fake) Resolve(_ context.Context, t Target) (Revisions, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(OpResolve); err != nil {
		return Revisions{}, err
	}
	r, ok := f.revisions[key(t)]
	if !ok {
		return Revisions{}, Errorf(ClassNotFound, "no change %s in %s", t.Change, t.Repository)
	}
	return r, nil
}

func (f *Fake) Changes(_ context.Context, t Target, r Revisions) ([]FileChange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(OpChanges); err != nil {
		return nil, err
	}
	files, ok := f.changes[at(t, r)]
	if !ok {
		return nil, Errorf(ClassNotFound, "no such revisions for %s", t.Change)
	}
	return append([]FileChange(nil), files...), nil
}

func (f *Fake) Publish(_ context.Context, s Submission) (Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, s)
	if err := f.failure(OpPublish); err != nil {
		return Receipt{}, err
	}
	f.seq++
	r := Receipt{Event: s.Event}
	r.ReviewID = "fk-" + strconv.Itoa(f.seq)
	r.URL = "https://fake.example/" + s.Target.Repository + "/" + s.Target.Change + "#" + r.ReviewID
	r.Identity, r.Revision, r.Formal = f.identity, s.Pinned.Head, s.Event != EventComment
	f.reviews[key(s.Target)] = append(f.reviews[key(s.Target)], fakeReview{r, s.Marker})
	if err := f.landThen; err != nil {
		f.landThen = nil
		return Receipt{}, err
	}
	return r, nil
}

func (f *Fake) Find(_ context.Context, t Target, e Expected) (Receipt, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(OpFind); err != nil {
		return Receipt{}, false, err
	}
	var matches []Receipt
	for _, r := range f.reviews[key(t)] {
		if strings.Contains(r.marker, e.Marker) {
			matches = append(matches, r.receipt)
		}
	}
	switch {
	case len(matches) == 0:
		return Receipt{}, false, nil
	case len(matches) > 1:
		return Receipt{}, false, Errorf(ClassAmbiguous, "%d reviews carry the marker", len(matches))
	case !Matches(matches[0], e):
		return Receipt{}, false, Errorf(ClassAmbiguous, "a review carries the marker but not the expected account, head or event")
	}
	return matches[0], true, nil
}
