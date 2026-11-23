package hub

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/pairbook"
	"github.com/pairtask/pairtask/pairbook/owner"
)

// The directory in this fixture serves maps and has no status endpoint.
// Each account has zero lineage on the wire and its own opaque selector.
func TestMapPeopleWithoutStatusKeepsOrgWorkspacesWithTheirAccounts(t *testing.T) {
	f := newRetryFixture(t)
	ctx := context.Background()
	ep, err := f.h.PairingEndpoint()
	messageWorkerOK(t, err)
	now := time.Now()
	_, ownerKey, err := ed25519.GenerateKey(rand.Reader)
	messageWorkerOK(t, err)
	anchor, err := owner.NewAnchor(ownerKey, now)
	messageWorkerOK(t, err)
	me, err := owner.New(ownerKey, anchor)
	messageWorkerOK(t, err)
	first, err := owner.NewSpace(me, owner.Genesis{Space: hubSpaceID, Name: "Org"}, now)
	messageWorkerOK(t, err)
	m := first.SpaceMap
	m.Format = pairbook.MapFormatOrg
	_, second, err := ed25519.GenerateKey(rand.Reader)
	messageWorkerOK(t, err)
	want := map[string]string{}
	for i, key := range []crypto.Signer{ep.Identity, second} {
		id := pairbook.PersonID{byte(i + 1)}
		name := []string{"Ada", "Ben"}[i]
		m.People = append(m.People, pairbook.MapPerson{Kind: pairbook.KindOrg, PersonID: id, Name: name, Role: pairbook.RoleMember})
		binding := rosterOrgBinding(t, id, key, now)
		m.Bindings = append(m.Bindings, binding)
		want[id.Selector()] = binding.Workspace()
	}
	all := pairbook.Selector{Kind: pairbook.SelectAll}
	m.Grants = []pairbook.Grant{{A: all, B: all}}
	f.pin.Owner = me.KeyID()
	messageWorkerOK(t, f.h.store.PinSpace(ctx, f.pin))
	messageWorkerOK(t, f.h.KeepSpaceMap(ctx, hubSpaceID, pairbook.SignSpaceMap(ownerKey, m)))
	step, refusal := f.h.mapPeople(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapPeople, Space: hubSpaceID})
	if refusal != nil {
		t.Fatal(refusal)
	}
	out, err := step.job(ctx, ctx)
	messageWorkerOK(t, err)
	if out.StatusKnown || len(out.People) != len(want) {
		t.Fatal("unexpected roster from map-only directory", out)
	}
	for _, person := range out.People {
		if len(person.Workspaces) != 1 || person.Workspaces[0].Workspace != want[person.Person] {
			t.Fatal("workspace grouped under incorrect account", person)
		}
		delete(want, person.Person)
	}
	if len(want) != 0 {
		t.Fatal("missing account selectors", want)
	}
}
func rosterOrgBinding(t *testing.T, id pairbook.PersonID, w crypto.Signer, at time.Time) pairbook.Binding {
	t.Helper()
	pub := w.Public().(ed25519.PublicKey)
	p := pairbook.OrgProof{Space: hubSpaceID, Workspace: pairbook.KeyID(pub), Nonce: [16]byte{1}, At: at.UnixMilli()}
	sig, err := w.Sign(rand.Reader, p.SigningBytes(), crypto.Hash(0))
	messageWorkerOK(t, err)
	proof := pairbook.SignedOrgProof{OrgProof: p, Sig: sig}
	return pairbook.Binding{Kind: pairbook.KindOrg, PersonID: id, WorkspaceKey: pub, Proof: proof.Marshal()}
}
