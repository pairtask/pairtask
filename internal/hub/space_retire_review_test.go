package hub

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

// This interleaving puts an already selected retirement candidate behind
// the activation of another space granting the same pair.
func TestSpaceRetirementRechecksGrantAtCommit(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	defer h.authority.close()
	signed, self, target := retireGrantFixture()
	pin := space.Pin{Space: signed.Space, Owner: pairbook.KeyID(signed.Owner), Base: hubDirectory, JoinedAt: time.Now()}
	if err := h.store.PinSpace(ctx, pin); err != nil {
		t.Fatal(err)
	}
	epoch := retirementCandidate(t, h, self, target)
	verified, err := h.store.KeepSpaceMap(ctx, pin.Space, signed, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h.authority.contactsMu.Lock()
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		retired, err := h.retireIfUngranted(ctx, self, target, epoch)
		if retired {
			t.Error("retired a contact now authorized by an overlapping space")
		}
		result <- err
	}()
	<-started
	notify := h.authority.activate(spaceKey(pin.Space), verified)
	h.authority.contactsMu.Unlock()
	h.authority.tell(notify, spaceKey(pin.Space))
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	managed, err := h.store.ManagedSpaceContacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if managed[target] != epoch {
		t.Fatal("managed generation changed")
	}
}
func retirementCandidate(t *testing.T, h *Hub, self, target string) int64 {
	t.Helper()
	ctx := context.Background()
	in := peer.SpaceInstall{
		Contact: peer.Contact{IdentityKey: target, Invited: true, Name: "Workspace", EstablishedAt: time.Now()},
		Space:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ConnectID: [16]byte{1}, PairingID: [32]byte{2}, Role: "inviter",
	}
	installed, err := h.store.InstallSpaceContact(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	before, err := h.desiredPeers(ctx, self)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before[target]; ok {
		t.Fatal("candidate was already granted")
	}
	return installed.Epoch
}

func retireGrantFixture() (pairbook.SignedSpaceMap, string, string) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	owner := hubSpaceKey("retire owner")
	anchor := identity.Anchor{Person: hubSpacePub(owner), InitAt: now.Add(-time.Hour)}.Signed(owner)
	lineage := anchor.Lineage()
	p := pairbook.MapPerson{Lineage: pairbook.Lineage(lineage), Person: hubSpacePub(owner), Role: pairbook.RoleOwner, Name: "Owner", Evidence: pairbook.PersonEvidence{Anchor: anchor.Marshal()}}
	binding := func(workspace ed25519.PrivateKey) pairbook.Binding {
		pub := hubSpacePub(workspace)
		claim := identity.Claim{Lineage: lineage, Epoch: 1, Workspace: pairbook.KeyID(pub), At: now}.Signed(owner)
		accepted := identity.Acceptance{Lineage: lineage, Epoch: 1, Person: pairbook.KeyID(p.Person), Workspace: pairbook.KeyID(pub), At: now}.Signed(workspace)
		return pairbook.Binding{Claim: claim.Marshal(), Acceptance: accepted.Marshal(), WorkspaceKey: pub}
	}
	a, b := binding(hubSpaceKey("retire local")), binding(hubSpaceKey("retire remote"))
	all := pairbook.Selector{Kind: pairbook.SelectAll}
	m := pairbook.SpaceMap{Space: hubSpaceID, Version: 1, IssuedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(),
		People: []pairbook.MapPerson{p}, Bindings: []pairbook.Binding{a, b}, Grants: []pairbook.Grant{{A: all, B: all}}}
	return pairbook.SignSpaceMap(owner, m), pairbook.KeyID(a.WorkspaceKey), pairbook.KeyID(b.WorkspaceKey)
}
