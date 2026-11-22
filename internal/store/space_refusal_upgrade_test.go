package store_test

import (
	"context"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestSpaceRefusalReplayAfterCodePairing(t *testing.T) {
	ctx := context.Background()
	f := installFixture(t)
	f.installedEpoch(t, 101)
	unpair := unpairRequest(t, f, "original-unpair")
	first, applied, err := f.db.UnpairContact(ctx, unpair)
	pairOK(t, err)
	pairEqual(t, applied, true)
	checkWorkspaceRefusal(t, f, true)

	f.installedEpoch(t, 102)
	checkWorkspaceRefusal(t, f, false)
	replay, applied, err := f.db.UnpairContact(ctx, unpair)
	pairOK(t, err)
	pairEqual(t, applied, false)
	pairEqual(t, replay, first)
	checkWorkspaceRefusal(t, f, false)
	confirmed, err := f.db.PeerMessages().Confirmed(ctx, f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, true)
}

func checkWorkspaceRefusal(t *testing.T, f *operatorContactFixture, want bool) {
	t.Helper()
	got, err := f.db.SpaceRefused(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, got, want)
}

// Reconstruct the C3b schema around a persisted contact in this test's
// temporary database. Its historic pairing rows and operator ledger survive.
func reopenFromC3b(t *testing.T, f *operatorContactFixture) {
	t.Helper()
	pairOK(t, f.db.Close())
	_, err := f.raw.Exec(`DROP TABLE space_refusals;
 DROP TABLE space_attempts;
 DROP TABLE space_contacts;
 ALTER TABLE pairing_exchanges DROP COLUMN space_pairing_id;
 DELETE FROM schema_steps WHERE name='space_refusals_seeded';`)
	pairOK(t, err)
	f.db = pairSQLite(t, f.path)
}

func TestSpaceRefusalUpgradeKeepsLocalUnpair(t *testing.T) {
	ctx := context.Background()
	f := installFixture(t)
	f.installedEpoch(t, 101)
	_, _, err := f.db.UnpairContact(ctx, unpairRequest(t, f, "before-upgrade"))
	pairOK(t, err)
	reopenFromC3b(t, f)
	checkWorkspaceRefusal(t, f, true)
	var source string
	pairOK(t, f.raw.QueryRow("SELECT source FROM space_refusals WHERE workspace=?", f.contact).Scan(&source))
	pairEqual(t, source, "seeded")
	in := peer.SpaceInstall{Contact: peer.Contact{IdentityKey: f.contact, Invited: true, Name: "Workspace", EstablishedAt: messageAt},
		Space: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ConnectID: [16]byte{1}, PairingID: [32]byte{2}, Role: "inviter"}
	_, err = f.db.InstallSpaceContact(ctx, in)
	pairEqual(t, err, store.ErrSpaceRefused)
}

func TestSpaceRefusalUpgradeDistinguishesPeerUnpair(t *testing.T) {
	f, c, id := controlStoreFixture(t)
	closed := controlAccept(t, c, messaging.ContactState{PairingID: id, Gen: 31, State: 3})
	pairEqual(t, closed.Close, true)
	reopenFromC3b(t, f)
	checkWorkspaceRefusal(t, f, false)
}
