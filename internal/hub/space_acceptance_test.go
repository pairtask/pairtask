package hub

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

func TestHubSpaceAcceptanceCreateUsesWorkspaceSignerAndPins(t *testing.T) {
	h := newTestHub(t)
	f := &fakeSpaceDirectory{}
	ctx := startSpaceHub(t, h, f)
	out := spaceSuccess(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpCreate, Directory: hubDirectory, ID: hubSpaceID, Name: "Office"}))
	hubSpaceCheck(t, f.createCalls == 1, "wrong owner result: %+v", out)
	hubSpaceCheck(t, len(out.Pins) == 1, "wrong owner result: %+v", out)
	hubSpaceCheck(t, out.Pins[0].Owner == h.identity.Fingerprint(), "wrong owner result: %+v", out)
	hubSpaceCheck(t, out.Pins[0].Owned, "wrong owner result: %+v", out)
	msg := []byte("workspace signer acceptance")
	sig, err := f.signer.Sign(nil, msg, crypto.Hash(0))
	hubSpaceCheck(t, err == nil, "%v", err)
	hubSpaceCheck(t, ed25519.Verify(h.identity.PublicKey(), msg, sig), "directory signer differs from workspace identity")
	if _, err := f.signer.Sign(nil, msg, crypto.SHA256); err == nil {
		t.Fatal("workspace signer ignored unsupported hash options")
	}
	requireHubPin(t, h, hubSpaceID, true)
	spaceSuccess(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpDelete, Space: hubSpaceID}))
	requireHubPin(t, h, hubSpaceID, false)
}

func TestHubSpaceAcceptanceAgentCannotOperateSpaces(t *testing.T) {
	h := newTestHub(t)
	f := &fakeSpaceDirectory{}
	ctx := startSpaceHub(t, h, f)
	agent := newClient("agent")
	h.Register(ctx, agent)
	recv(t, agent)
	h.Submit(ctx, Inbound{From: agent, Msg: protocol.Message{Type: protocol.TypeOperatorSpace, RequestID: "agent-space", SpaceRequest: &protocol.SpaceRequest{Op: protocol.SpaceOpCreate, ID: hubSpaceID, Directory: hubDirectory}}})
	got := recv(t, agent)
	hubSpaceCheck(t, got.Type == protocol.TypeError, "agent crossed operator boundary: %+v", got)
	hubSpaceCheck(t, got.Code == protocol.ErrForbidden, "agent crossed operator boundary: %+v", got)
	hubSpaceCheck(t, f.createCalls == 0, "agent caused directory mutation")
}

func TestHubSpaceAcceptanceConflictingJoinNeverCallsDirectory(t *testing.T) {
	h := newTestHub(t)
	f := &fakeSpaceDirectory{}
	ctx := startSpaceHub(t, h, f)
	original := hubPin(pairbook.KeyID(hubSpacePub(hubSpaceKey("owner"))))
	saveHubPin(t, h, original)
	inv := space.Invite{Base: hubDirectory, Space: hubSpaceID, Owner: pairbook.KeyID(hubSpacePub(hubSpaceKey("other")))}
	spaceRefused(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpJoin, Link: inv.String()}))
	hubSpaceCheck(t, f.joinCalls == 0, "conflicting join sent token before checking local trust")
	inv.Owner = original.Owner
	inv.Base = "https://different.example"
	spaceRefused(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpJoin, Link: inv.String()}))
	hubSpaceCheck(t, f.joinCalls == 0, "conflicting directory was contacted")
	held, _, err := h.store.SpacePin(ctx, hubSpaceID)
	hubSpaceCheck(t, err == nil, "conflict replaced pin")
	hubSpaceCheck(t, held.Owner == original.Owner, "conflict replaced pin")
	hubSpaceCheck(t, held.Base == original.Base, "conflict replaced pin")
}

func TestHubSpaceAcceptanceLeaveUnpinsOnlyAfterRemoteSuccess(t *testing.T) {
	h := newTestHub(t)
	f := &fakeSpaceDirectory{}
	ctx := startSpaceHub(t, h, f)
	saveHubPin(t, h, hubPin(pairbook.KeyID(hubSpacePub(hubSpaceKey("owner")))))
	f.fail = true
	spaceRefused(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpLeave, Space: hubSpaceID}))
	requireHubPin(t, h, hubSpaceID, true)
	f.fail = false
	spaceSuccess(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpLeave, Space: hubSpaceID}))
	requireHubPin(t, h, hubSpaceID, false)
	hubSpaceCheck(t, pairbook.KeyID(f.revoked) == h.identity.Fingerprint(), "leave revoked another member")
}

func TestHubSpaceAcceptanceOwnerCannotLeaveAndFailedDeleteKeepsPin(t *testing.T) {
	h := newTestHub(t)
	f := &fakeSpaceDirectory{}
	ctx := startSpaceHub(t, h, f)
	saveHubPin(t, h, hubPin(h.identity.Fingerprint()))
	spaceRefused(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpLeave, Space: hubSpaceID}))
	hubSpaceCheck(t, f.revokeCalls == 0, "owner leave sent revocation")
	f.fail = true
	spaceRefused(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpDelete, Space: hubSpaceID}))
	requireHubPin(t, h, hubSpaceID, true)
}

func TestHubSpaceAcceptanceApprovalRequiresSeenVerifiedRequest(t *testing.T) {
	h := newTestHub(t)
	f := &fakeSpaceDirectory{}
	ctx := startSpaceHub(t, h, f)
	saveHubPin(t, h, hubPin(h.identity.Fingerprint()))
	node := hubSpacePub(hubSpaceKey("node"))
	key := pairbook.KeyID(node)
	spaceRefused(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpApprove, Node: key}))
	hubSpaceCheck(t, f.approveCalls == 0, "unseen node countersigned")
	f.requests = []pairbook.JoinRequest{{Space: hubSpaceID, Node: node, Name: "Alice", At: hubSpaceNow.UnixMilli()}}
	spaceSuccess(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpApprove, Node: key}))
	hubSpaceCheck(t, f.approveCalls == 1, "approved wrong request")
	hubSpaceCheck(t, f.approved.Name == "Alice", "approved wrong request")
	hubSpaceCheck(t, pairbook.KeyID(f.approved.Node) == key, "approved wrong request")
}

func TestHubSpaceAcceptanceAmbiguousSelectionRequiresSpace(t *testing.T) {
	h := newTestHub(t)
	f := &fakeSpaceDirectory{}
	ctx := startSpaceHub(t, h, f)
	a := hubPin(h.identity.Fingerprint())
	b := a
	b.Space = strings.Repeat("b", 32)
	saveHubPin(t, h, a)
	saveHubPin(t, h, b)
	spaceRefused(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMembers}))
	out := spaceSuccess(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpList}))
	hubSpaceCheck(t, len(out.Pins) == 2, "space list lost pins")
	spaceSuccess(t, spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMembers, Space: a.Space}))
}

func TestHubSpaceAcceptanceSlowDirectoryDoesNotBlockLocalList(t *testing.T) {
	h := newTestHub(t)
	started, release := make(chan struct{}), make(chan struct{})
	f := &fakeSpaceDirectory{members: func(ctx context.Context) ([]pairbook.Member, error) {
		close(started)
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	ctx := startSpaceHub(t, h, f)
	saveHubPin(t, h, hubPin(h.identity.Fingerprint()))
	pending := make(chan protocol.Message, 1)
	go func() { pending <- spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMembers}) }()
	awaitSpaceDirectoryStart(t, started, pending)
	list := make(chan protocol.Message, 1)
	go func() { list <- spaceOp(h, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpList}) }()
	var got protocol.Message
	blocked := false
	select {
	case got = <-list:
	case <-time.After(2 * time.Second):
		blocked = true
	}
	close(release)
	addressAwait(t, pending)
	if blocked {
		addressAwait(t, list)
		t.Fatal("directory I/O blocked hub Run loop")
	}
	spaceSuccess(t, got)
}

func TestHubSpaceAcceptancePinsSurviveHubRestart(t *testing.T) {
	h := newTestHub(t)
	f := &fakeSpaceDirectory{}
	if err := h.loadIdentity(context.Background()); err != "" {
		t.Fatal(err)
	}
	saveHubPin(t, h, hubPin(h.identity.Fingerprint()))
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	cancel()
	addressAwait(t, h.Done())
	restarted := New(h.store, 0, h.log)
	ctx = startSpaceHub(t, restarted, f)
	got := spaceSuccess(t, spaceOp(restarted, ctx, protocol.SpaceRequest{Op: protocol.SpaceOpList}))
	hubSpaceCheck(t, len(got.Pins) == 1, "restart changed space pins: %+v", got)
	hubSpaceCheck(t, got.Pins[0].Space == hubSpaceID, "restart changed space pins: %+v", got)
	hubSpaceCheck(t, got.Pins[0].Owner == h.identity.Fingerprint(), "restart changed space pins: %+v", got)
}

func hubSpaceCheck(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

func awaitSpaceDirectoryStart(t *testing.T, started <-chan struct{}, pending <-chan protocol.Message) {
	t.Helper()
	select {
	case <-started:
	case m := <-pending:
		t.Fatalf("directory was never called: type=%s code=%s detail=%s", m.Type, m.Code, m.Detail)
	case <-time.After(2 * time.Second):
		t.Fatal("directory call never started")
	}
}
