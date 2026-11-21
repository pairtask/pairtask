package store_test

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pairtask/pairtask/internal/identity"
)

func nextEpochPerson(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	raw, err := os.ReadFile("../identity/testdata/wire-v1.json")
	pairOK(t, err)
	var packet struct {
		Fixtures map[string]struct {
			Seed string `json:"public_test_seed_hex"`
		}
	}
	pairOK(t, json.Unmarshal(raw, &packet))
	seed, err := hex.DecodeString(packet.Fixtures["P1"].Seed)
	pairOK(t, err)
	return ed25519.NewKeyFromSeed(seed)
}

func TestIdentityStoreResubmissionAfterDeferredEviction(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "deferred.db")
	db := pairSQLite(t, path)
	contact := messageContact(t, db, 1)
	wires, anchor, _ := replayFixture(t)
	bound, err := db.PeerMessages().Admit(ctx, contact)
	pairOK(t, err)
	apply := func(wire []byte, want identity.EffectKind) {
		t.Helper()
		effect, err := bound.ApplyIdentity(ctx, wire, messageAt)
		pairOK(t, err)
		pairEqual(t, effect.Kind, want)
	}
	apply(wires["anchor"], identity.Applied)
	pairOK(t, db.ApproveIdentity(ctx, anchor.Lineage(), messageAt))
	person := nextEpochPerson(t)
	next := identity.Record{Lineage: anchor.Lineage(), Epoch: 2, Counter: 1, Recovery: identity.KeyID(anchor.Recovery), Person: identity.KeyID(person.Public().(ed25519.PublicKey)), At: messageAt}
	signed := func(version uint64) identity.Record { next.Version = version; return next.Signed(person) }
	newest := signed(identity.MaxParked + 1)
	apply(newest.Marshal(), identity.Parked)
	for version := 1; version <= identity.MaxParked; version++ {
		apply(signed(uint64(version)).Marshal(), identity.Parked)
	}
	apply(wires["succession-1"], identity.Applied)
	held, found, err := db.HeldIdentity(ctx, anchor.Lineage())
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, held.Version, uint64(identity.MaxParked))
	apply(newest.Marshal(), identity.Applied)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	held, found, err = db.HeldIdentity(ctx, anchor.Lineage())
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, held.RecordDigest, identity.DigestOf(newest.TBS()))
}
