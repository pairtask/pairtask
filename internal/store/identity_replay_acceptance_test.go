package store_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/identity"
)

func TestIdentityStoreRestartRetainsParkedAndFrozenProofs(t *testing.T) {
	raw, err := os.ReadFile("../identity/testdata/wire-v1.json")
	pairOK(t, err)
	var fixture struct {
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
		if !found || held.Seq != 3 || len(held.Parked) != 0 {
			t.Fatalf("pending proof did not advance: seq=%d parked=%d", held.Seq, len(held.Parked))
		}
		apply("fork-at-anchor", identity.FrozenEffect)
		reopen()
		held, found, err = db.HeldIdentity(ctx, anchor.Lineage())
		pairOK(t, err)
		if !found || held.Trust != identity.Frozen || len(held.Conflicts) == 0 {
			t.Fatal("lost authenticated conflict")
		}
		return held
	}
	control := run(t, false)
	restored := run(t, true)
	if !reflect.DeepEqual(control, restored) {
		t.Fatal("restarted store differs from uninterrupted signed proof history")
	}
}
