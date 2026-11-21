package store_test

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/identity"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/signing"
	"github.com/pairtask/pairtask/internal/store"
)

func admissionSigningKeys(t *testing.T) (device, person ed25519.PrivateKey) {
	t.Helper()
	raw, err := os.ReadFile("../identity/testdata/wire-v1.json")
	pairOK(t, err)
	var fixture struct {
		Fixtures map[string]struct {
			Seed string `json:"public_test_seed_hex"`
		}
	}
	pairOK(t, json.Unmarshal(raw, &fixture))
	key := func(name string) ed25519.PrivateKey {
		seed, err := hex.DecodeString(fixture.Fixtures[name].Seed)
		pairOK(t, err)
		return ed25519.NewKeyFromSeed(seed)
	}
	return key("D0"), key("P0")
}

func TestIdentityAdmissionRollbackAndCurrentVerification(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "admission.db")
	db := pairSQLite(t, path)
	wires, anchor, w := replayFixture(t)
	own := joinKey(90)
	pairOK(t, db.InitIdentity(ctx, own))
	contact := identity.KeyID(w)
	outcome := pairOutcome(1)
	outcome.IdentityKey = contact
	pairSaved(t, db, outcome)
	_, err := db.Accept(ctx, contact)
	pairOK(t, err)
	_, err = db.Deliver(ctx, contact, peer.PairingAccepted, messageAt)
	pairOK(t, err)
	bound, err := db.PeerMessages().Admit(ctx, contact)
	pairOK(t, err)
	apply := func(name string) {
		t.Helper()
		effect, err := bound.ApplyIdentity(ctx, wires[name], messageAt)
		pairOK(t, err)
		pairEqual(t, effect.Kind, identity.Applied)
	}
	apply("anchor")
	pairOK(t, db.ApproveIdentity(ctx, anchor.Lineage(), messageAt))
	pairOK(t, db.NoteWorkspaceKey(ctx, w, messageAt))
	apply("record")
	cert, err := signing.ParseCert(wires["device"])
	pairOK(t, err)
	device, person := admissionSigningKeys(t)
	m := messageWire("atomic-admission", 1)
	m.CreatedAt = cert.NotBefore
	link := protocol.PeerLink{Contact: contact, Receiver: own}
	header := signing.Header{MessageID: m.ID, SenderKind: signing.SenderOperator, SenderID: protocol.OperatorID, Workspace: contact, Recipients: link.Recipients(), CreatedAt: m.CreatedAt, TextHash: signing.TextHash(m.Text)}
	m.Seal = signing.Sign(device, []signing.Cert{cert}, anchor.Person, header).Marshal()
	now := cert.NotBefore.Add(time.Minute)
	defer store.SetIdentityClock(func() time.Time { return now })()
	failure := errors.New("injected commit failure")
	restore := store.SetAdmissionHook(func(stage string) error {
		if stage == "before-commit" {
			return failure
		}
		return nil
	})
	_, err = bound.Accept(ctx, contact, m, now)
	restore()
	if !errors.Is(err, failure) {
		t.Fatalf("failed to reach commit hook: %v", err)
	}
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	bound, err = db.PeerMessages().Admit(ctx, contact)
	pairOK(t, err)
	_, _, found, err := bound.Admission(ctx, contact, m.ID)
	pairOK(t, err)
	pairEqual(t, found, false)
	pairEqual(t, len(messagePage(t, db.PeerMessages(), contact)), 0)
	now = cert.NotBefore
	_, err = bound.Accept(ctx, contact, m, now)
	pairOK(t, err)
	current, evidence, found, err := bound.Admission(ctx, contact, m.ID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, current.State, signing.Verified)
	pairEqual(t, evidence.State, signing.Verified)
	pairEqual(t, evidence.AdmittedAt.Equal(now), true)
	revoke := identity.Revocation{Lineage: anchor.Lineage(), Epoch: 1, Device: evidence.Device, At: now}
	wires["revoke-admitted-device"] = revoke.Signed(person).Marshal()
	apply("revoke-admitted-device")
	current, after, found, err := bound.Admission(ctx, contact, m.ID)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, current.State, signing.Invalid)
	pairEqual(t, after, evidence)
}
