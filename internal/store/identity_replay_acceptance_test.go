package store_test

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/identity"
)

func replayFixture(t *testing.T) (map[string][]byte, identity.Anchor, ed25519.PublicKey) {
	t.Helper()
	raw, err := os.ReadFile("../identity/testdata/wire-v1.json")
	pairOK(t, err)
	var fixture struct {
		Fixtures map[string]struct {
			Public string `json:"public_key_hex"`
		}
		Vectors []struct {
			Name string
			Wire string `json:"wire_hex"`
		}
	}
	pairOK(t, json.Unmarshal(raw, &fixture))
	wires := map[string][]byte{}
	for _, v := range fixture.Vectors {
		wires[v.Name], err = hex.DecodeString(v.Wire)
		pairOK(t, err)
	}
	anchor, err := identity.ParseAnchor(wires["anchor"])
	pairOK(t, err)
	workspace, err := hex.DecodeString(fixture.Fixtures["W0"].Public)
	pairOK(t, err)
	return wires, anchor, workspace
}

func TestIdentityStoreRestartRetainsParkedAndFrozenProofs(t *testing.T) {
	wires, anchor, _ := replayFixture(t)
	run := func(t *testing.T, restart bool) identity.Held {
		path := filepath.Join(t.TempDir(), "identity.db")
		db := pairSQLite(t, path)
		contact := messageContact(t, db, 1)
		ctx := context.Background()
		apply := func(name string, want identity.EffectKind) {
			t.Helper()
			peer, err := db.PeerMessages().Admit(ctx, contact)
			pairOK(t, err)
			effect, err := peer.ApplyIdentity(ctx, wires[name], messageAt)
			pairOK(t, err)
			if effect.Kind != want {
				t.Fatalf("%s: got %+v, want %s", name, effect, want)
			}
		}
		reopen := func() {
			if restart {
				pairOK(t, db.Close())
				db = pairSQLite(t, path)
			}
		}
		apply("anchor", identity.Applied)
		pairOK(t, db.ApproveIdentity(ctx, anchor.Lineage(), messageAt))
		apply("succession-3", identity.Parked)
		reopen()
		apply("succession-1", identity.Applied)
		apply("handover-2", identity.Applied)
		held, found, err := db.HeldIdentity(ctx, anchor.Lineage())
		pairOK(t, err)
		pairEqual(t, found, true)
		pairEqual(t, held.Seq, 3)
		pairEqual(t, len(held.Parked), 0)
		apply("fork-at-anchor", identity.FrozenEffect)
		reopen()
		held, found, err = db.HeldIdentity(ctx, anchor.Lineage())
		pairOK(t, err)
		pairEqual(t, found, true)
		pairEqual(t, held.Trust, identity.Frozen)
		pairEqual(t, len(held.Conflicts) > 0, true)
		return held
	}
	control := run(t, false)
	restored := run(t, true)
	if !reflect.DeepEqual(control, restored) {
		t.Fatal("restarted store differs from uninterrupted signed proof history")
	}
}

func TestIdentityStoreRetryAfterLearningWorkspaceKey(t *testing.T) {
	wires, anchor, workspace := replayFixture(t)
	path := filepath.Join(t.TempDir(), "retry.db")
	ctx := context.Background()
	db := pairSQLite(t, path)
	contact := messageContact(t, db, 1)
	apply := func(name string, want identity.EffectKind) {
		t.Helper()
		peer, err := db.PeerMessages().Admit(ctx, contact)
		pairOK(t, err)
		effect, err := peer.ApplyIdentity(ctx, wires[name], messageAt)
		pairOK(t, err)
		if effect.Kind != want {
			t.Fatalf("%s: got %+v, want %s", name, effect, want)
		}
	}
	apply("anchor", identity.Applied)
	pairOK(t, db.ApproveIdentity(ctx, anchor.Lineage(), messageAt))
	apply("record", identity.Rejected)
	pairOK(t, db.NoteWorkspaceKey(ctx, workspace, messageAt))
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	apply("record", identity.Applied)
	apply("record", identity.Duplicate)
}
