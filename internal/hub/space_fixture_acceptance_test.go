package hub

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

const hubSpaceID = "0123456789abcdef0123456789abcdef"
const hubDirectory = "https://directory.example"

var hubSpaceSeq atomic.Uint64

var hubSpaceNow = time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)

func hubSpaceKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("hub space " + label))
	return ed25519.NewKeyFromSeed(seed[:])
}
func hubSpacePub(key ed25519.PrivateKey) ed25519.PublicKey { return key.Public().(ed25519.PublicKey) }

type fakeSpaceDirectory struct {
	space.Directory
	signer                                                         crypto.Signer
	createCalls, joinCalls, approveCalls, revokeCalls, deleteCalls int
	fail                                                           bool
	requests                                                       []pairbook.JoinRequest
	approved                                                       pairbook.JoinRequest
	revoked                                                        ed25519.PublicKey
	members                                                        func(context.Context) ([]pairbook.Member, error)
}

func (f *fakeSpaceDirectory) failure() error {
	if f.fail {
		return errors.New("directory unavailable")
	}
	return nil
}
func (f *fakeSpaceDirectory) Create(_ context.Context, id, name string) (space.Pin, error) {
	f.createCalls++
	return space.Pin{Space: id, Owner: pairbook.KeyID(f.signer.Public().(ed25519.PublicKey)), Base: hubDirectory, JoinedAt: hubSpaceNow}, f.failure()
}
func (f *fakeSpaceDirectory) Join(_ context.Context, inv space.Invite, name string) (space.Pin, error) {
	f.joinCalls++
	return space.Pin{Space: inv.Space, Owner: inv.Owner, Base: inv.Base, JoinedAt: hubSpaceNow}, f.failure()
}
func (f *fakeSpaceDirectory) Requests(context.Context, space.Pin) ([]pairbook.JoinRequest, error) {
	return f.requests, f.failure()
}
func (f *fakeSpaceDirectory) Approve(_ context.Context, p space.Pin, r pairbook.JoinRequest, d time.Duration) error {
	f.approveCalls++
	f.approved = r
	return f.failure()
}
func (f *fakeSpaceDirectory) Members(ctx context.Context, p space.Pin) ([]pairbook.Member, error) {
	if f.members != nil {
		return f.members(ctx)
	}
	return nil, f.failure()
}
func (f *fakeSpaceDirectory) Revoke(_ context.Context, p space.Pin, node ed25519.PublicKey) error {
	f.revokeCalls++
	f.revoked = node
	return f.failure()
}
func (f *fakeSpaceDirectory) Delete(context.Context, space.Pin) error {
	f.deleteCalls++
	return f.failure()
}
func startSpaceHub(t *testing.T, h *Hub, f *fakeSpaceDirectory) context.Context {
	t.Helper()
	if err := h.loadIdentity(context.Background()); err != "" {
		t.Fatal(err)
	}
	h.SetSpaces(SpaceConfig{Clock: func() time.Time { return hubSpaceNow }, Directory: func(base string, signer crypto.Signer) (space.Directory, error) { f.signer = signer; return f, nil }})
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(func() { cancel(); addressAwait(t, h.Done()) })
	return ctx
}
func spaceOp(h *Hub, ctx context.Context, r protocol.SpaceRequest) protocol.Message {
	return h.SubmitOperator(ctx, 0, protocol.Message{Type: protocol.TypeOperatorSpace, MessageID: fmt.Sprintf("space-%s-%d", r.Op, hubSpaceSeq.Add(1)), SpaceRequest: &r})
}
func spaceSuccess(t *testing.T, m protocol.Message) *protocol.SpaceResult {
	t.Helper()
	if m.Type != protocol.TypeOperatorSpaceResult || m.SpaceResult == nil {
		t.Fatalf("space failed: type=%s code=%s detail=%s", m.Type, m.Code, m.Detail)
	}
	return m.SpaceResult
}
func spaceRefused(t *testing.T, m protocol.Message) {
	t.Helper()
	if m.Type != protocol.TypeError {
		t.Fatalf("space unexpectedly succeeded: type=%s", m.Type)
	}
}
func hubPin(owner string) space.Pin {
	return space.Pin{Space: hubSpaceID, Owner: owner, Base: hubDirectory, JoinedAt: hubSpaceNow}
}
func saveHubPin(t *testing.T, h *Hub, p space.Pin) {
	t.Helper()
	if err := h.store.PinSpace(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}
func requireHubPin(t *testing.T, h *Hub, id string, want bool) {
	t.Helper()
	_, ok, err := h.store.SpacePin(context.Background(), id)
	if err != nil || ok != want {
		t.Fatalf("pin exists=%v want=%v err=%v", ok, want, err)
	}
}
