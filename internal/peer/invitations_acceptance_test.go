package peer_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

func invOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func invError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("operation unexpectedly succeeded")
	}
}
func invEqual[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

type invitationClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *invitationClock) read() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *invitationClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type invitationFixture struct {
	manager *peer.Invitations
	clock   *invitationClock
	store   peer.PairingStore
}

func newInvitationFixture(t *testing.T, store peer.PairingStore) invitationFixture {
	t.Helper()
	clock := &invitationClock{now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	manager, err := peer.NewInvitations(peer.DefaultInvitationPolicy(), clock.read, rand.Reader, store)
	invOK(t, err)
	return invitationFixture{manager, clock, store}
}
func openInvitation(t *testing.T, f invitationFixture) peer.Invitation {
	t.Helper()
	value, err := f.manager.Open()
	invOK(t, err)
	return value
}
func beginInvitation(t *testing.T, f invitationFixture, id string) *peer.Attempt {
	t.Helper()
	attempt, err := f.manager.Begin(id)
	invOK(t, err)
	return attempt
}
func pairedPeer() peer.PairedPeer {
	return peer.PairedPeer{IdentityKey: strings.Repeat("a", 64), Name: "Alice", TranscriptID: bytes.Repeat([]byte{9}, 64)}
}
func invitationStatus(t *testing.T, f invitationFixture) peer.InvitationStatus {
	t.Helper()
	status, ok := f.manager.Status()
	invEqual(t, ok, true)
	return status
}

func TestInvitationAcceptanceBudgetAndBusy(t *testing.T) {
	f := newInvitationFixture(t, peer.NewMemPairingStore())
	policy := peer.DefaultInvitationPolicy()
	invEqual(t, policy.Digits, 6)
	invEqual(t, policy.MaxAttempts, 3)
	invEqual(t, policy.TTL, 10*time.Minute)
	invEqual(t, policy.AttemptTimeout, 30*time.Second)
	invitation := openInvitation(t, f)
	code := strings.ReplaceAll(invitation.Code, "-", "")
	invEqual(t, len(code), 6)
	invEqual(t, strings.Trim(code, "0123456789"), "")
	invEqual(t, invitation.ExpiresAt.Equal(f.clock.read().Add(policy.TTL)), true)
	first := beginInvitation(t, f, invitation.ID)
	copied := first.Code()
	clear(copied)
	invEqual(t, len(first.Code()) > 0, true)
	invEqual(t, bytes.Equal(copied, first.Code()), false)
	_, err := f.manager.Begin(invitation.ID)
	invEqual(t, errors.Is(err, peer.ErrInvitationBusy), true)
	invEqual(t, invitationStatus(t, f).AttemptsLeft, 2)
	invOK(t, first.Fail())
	for remaining := 1; remaining >= 0; remaining-- {
		attempt := beginInvitation(t, f, invitation.ID)
		invEqual(t, invitationStatus(t, f).AttemptsLeft, remaining)
		invOK(t, attempt.Fail())
	}
	_, err = f.manager.Begin(invitation.ID)
	invEqual(t, errors.Is(err, peer.ErrInvitationExhausted), true)
}

func TestInvitationAcceptanceOneConcurrentAttempt(t *testing.T) {
	f := newInvitationFixture(t, peer.NewMemPairingStore())
	invitation := openInvitation(t, f)
	var wg sync.WaitGroup
	winners := make(chan *peer.Attempt, 16)
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := f.manager.Begin(invitation.ID)
			if err != nil {
				errs <- err
				return
			}
			winners <- a
		}()
	}
	wg.Wait()
	close(winners)
	close(errs)
	invEqual(t, len(winners), 1)
	invEqual(t, len(errs), 15)
	for err := range errs {
		invEqual(t, errors.Is(err, peer.ErrInvitationBusy), true)
	}
	invEqual(t, invitationStatus(t, f).AttemptsLeft, 2)
	invOK(t, (<-winners).Fail())
}

func checkLateInvitation(t *testing.T, invalidate func(*testing.T, invitationFixture, string)) {
	t.Helper()
	f := newInvitationFixture(t, peer.NewMemPairingStore())
	invitation := openInvitation(t, f)
	attempt := beginInvitation(t, f, invitation.ID)
	invalidate(t, f, invitation.ID)
	_, err := attempt.Confirm(context.Background(), pairedPeer())
	invError(t, err)
	rows, err := f.store.Requests(context.Background())
	invOK(t, err)
	invEqual(t, len(rows), 0)
}

func TestInvitationAcceptanceLateCompletionRefused(t *testing.T) {
	cases := map[string]func(*testing.T, invitationFixture, string){
		"cancel":  func(t *testing.T, f invitationFixture, id string) { invOK(t, f.manager.Cancel(id)) },
		"replace": func(t *testing.T, f invitationFixture, id string) { invEqual(t, openInvitation(t, f).ID == id, false) },
		"expiry-boundary": func(t *testing.T, f invitationFixture, _ string) {
			f.clock.advance(10 * time.Minute)
			status := invitationStatus(t, f)
			invEqual(t, status.Expired, true)
			invEqual(t, status.InFlight, false)
		},
		"attempt-timeout-boundary": func(_ *testing.T, f invitationFixture, _ string) { f.clock.advance(30 * time.Second) },
		"clock-rollback":           func(_ *testing.T, f invitationFixture, _ string) { f.clock.advance(-time.Second) },
	}
	for name, invalidate := range cases {
		t.Run(name, func(t *testing.T) { checkLateInvitation(t, invalidate) })
	}
}

func TestInvitationAcceptanceTimeoutDoesNotRefund(t *testing.T) {
	f := newInvitationFixture(t, peer.NewMemPairingStore())
	invitation := openInvitation(t, f)
	old := beginInvitation(t, f, invitation.ID)
	f.clock.advance(30 * time.Second)
	next := beginInvitation(t, f, invitation.ID)
	invEqual(t, invitationStatus(t, f).AttemptsLeft, 1)
	invEqual(t, next.ID() == old.ID(), false)
	_, err := old.Confirm(context.Background(), pairedPeer())
	invError(t, err)
	invOK(t, next.Fail())
}

type failingPairingStore struct {
	peer.PairingStore
	fail  bool
	calls int
}

func (s *failingPairingStore) Commit(ctx context.Context, value peer.PairingOutcome) (peer.PairingRequest, error) {
	s.calls++
	if s.fail {
		return peer.PairingRequest{}, io.ErrClosedPipe
	}
	return s.PairingStore.Commit(ctx, value)
}

func TestInvitationAcceptanceStoreFailureRetriesIdenticalOutcome(t *testing.T) {
	store := &failingPairingStore{PairingStore: peer.NewMemPairingStore(), fail: true}
	f := newInvitationFixture(t, store)
	invitation := openInvitation(t, f)
	attempt := beginInvitation(t, f, invitation.ID)
	firstAt := f.clock.read()
	original := pairedPeer()
	_, err := attempt.Confirm(context.Background(), original)
	invEqual(t, errors.Is(err, io.ErrClosedPipe), true)
	invEqual(t, invitationStatus(t, f).Consumed, false)
	invEqual(t, invitationStatus(t, f).AttemptsLeft, 2)
	altered := pairedPeer()
	altered.TranscriptID[0] ^= 1
	_, err = attempt.Confirm(context.Background(), altered)
	invEqual(t, errors.Is(err, peer.ErrOutcomeConflict), true)
	invEqual(t, store.calls, 1)
	// A caller must not be able to mutate the frozen retry content through
	// the slice originally passed to Confirm.
	clear(original.TranscriptID)
	f.clock.advance(time.Second)
	store.fail = false
	request, err := attempt.Confirm(context.Background(), pairedPeer())
	invOK(t, err)
	invEqual(t, request.State, peer.PairingPending)
	outcome, ok, err := store.Outcome(context.Background(), attempt.ID())
	invOK(t, err)
	invEqual(t, ok, true)
	invEqual(t, outcome.InvitationID, invitation.ID)
	invEqual(t, outcome.At.Equal(firstAt), true)
	invEqual(t, invitationStatus(t, f).Consumed, true)
	_, err = f.manager.Begin(invitation.ID)
	invEqual(t, errors.Is(err, peer.ErrInvitationConsumed), true)
}

func TestInvitationAcceptanceRestartInvalidatesOnlyInvitations(t *testing.T) {
	store := peer.NewMemPairingStore()
	old := newInvitationFixture(t, store)
	invitation := openInvitation(t, old)
	attempt := beginInvitation(t, old, invitation.ID)
	_, err := attempt.Confirm(context.Background(), pairedPeer())
	invOK(t, err)
	unfinished := openInvitation(t, old)
	restarted := newInvitationFixture(t, store)
	_, err = restarted.manager.Begin(unfinished.ID)
	invEqual(t, errors.Is(err, peer.ErrNoInvitation), true)
	outcome, ok, err := store.Outcome(context.Background(), attempt.ID())
	invOK(t, err)
	invEqual(t, ok, true)
	invEqual(t, outcome.IdentityKey, pairedPeer().IdentityKey)
	row, ok, err := store.Request(context.Background(), outcome.IdentityKey)
	invOK(t, err)
	invEqual(t, ok, true)
	invEqual(t, row.State, peer.PairingPending)
}

func TestInvitationAcceptanceEntropyFailureCreatesNothing(t *testing.T) {
	store := peer.NewMemPairingStore()
	manager, err := peer.NewInvitations(peer.DefaultInvitationPolicy(), time.Now, bytes.NewReader(nil), store)
	invOK(t, err)
	_, err = manager.Open()
	invError(t, err)
	_, ok := manager.Status()
	invEqual(t, ok, false)
	rows, err := store.Requests(context.Background())
	invOK(t, err)
	invEqual(t, len(rows), 0)
}

func TestInvitationAcceptanceCodeExcludedFromSerialization(t *testing.T) {
	// Known entropy keeps the secret distinct from the ID and timestamp.
	entropy := append(bytes.Repeat([]byte{0xab}, 16), make([]byte, 8)...)
	entropy[len(entropy)-1] = 1
	manager, err := peer.NewInvitations(peer.DefaultInvitationPolicy(), time.Now,
		bytes.NewReader(entropy), peer.NewMemPairingStore())
	invOK(t, err)
	invitation, err := manager.Open()
	invOK(t, err)
	invEqual(t, invitation.Code, "000-001")
	encoded, err := json.Marshal(invitation)
	invOK(t, err)
	for _, output := range []string{string(encoded), fmt.Sprintf("%v", invitation),
		fmt.Sprintf("%+v", invitation), fmt.Sprintf("%#v", invitation)} {
		invEqual(t, strings.Contains(output, invitation.Code), false)
	}
}
