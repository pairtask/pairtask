package hub

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

type unavailableConnectDirectory struct {
	space.Directory
	members []pairbook.Member
}

func (d unavailableConnectDirectory) Members(context.Context, space.Pin) ([]pairbook.Member, error) {
	return d.members, nil
}
func (d unavailableConnectDirectory) Address(_ context.Context, p space.Pin, node ed25519.PublicKey) (pairbook.Address, error) {
	return pairbook.Address{Space: p.Space, Node: node, Addrs: []string{"127.0.0.1:1"}}, nil
}

func TestHubSpaceConnectReviewMissingDialerRefusesWithoutPanic(t *testing.T) {
	h := newTestHub(t)
	ctx := context.Background()
	hubSpaceCheck(t, h.loadIdentity(ctx) == "", "identity unavailable")
	target := hubSpacePub(hubSpaceKey("connect-target"))
	d := unavailableConnectDirectory{members: []pairbook.Member{{Space: hubSpaceID, Node: h.identity.PublicKey()}, {Space: hubSpaceID, Node: target}}}
	h.SetSpaces(SpaceConfig{Directory: func(string, crypto.Signer) (space.Directory, error) { return d, nil }})
	saveHubPin(t, h, hubPin(h.identity.Fingerprint()))
	// Invoke the worker synchronously so a missing Dial is reported as a
	// failed test rather than terminating the entire process in a goroutine.
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("connect panicked without a dialer: %v", recovered)
		}
	}()
	step, refusal := h.spaceConnect(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpConnect, Space: hubSpaceID, Node: pairbook.KeyID(target)})
	if refusal != nil {
		return
	}
	hubSpaceCheck(t, step.job != nil, "connect neither refused nor supplied a worker")
	_, err := step.job(ctx, ctx)
	hubSpaceCheck(t, err != nil, "connect succeeded without a dialer")
}
