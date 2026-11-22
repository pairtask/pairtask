package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/store"
)

func TestManagedContactRetirementAndDurableRefusal(t *testing.T) {
	ctx := context.Background()
	f := installFixture(t)
	in := peer.SpaceInstall{
		Contact: peer.Contact{IdentityKey: f.contact, Name: "Workspace", EstablishedAt: messageAt, Invited: true},
		Space:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ConnectID: [16]byte{1}, PairingID: sha256.Sum256([]byte("first space pairing")), Role: "inviter",
	}
	first, err := f.db.InstallSpaceContact(ctx, in)
	pairOK(t, err)
	managed, err := f.db.ManagedSpaceContacts(ctx)
	pairOK(t, err)
	pairEqual(t, managed[f.contact], first.Epoch)
	pairEqual(t, epochCount(t, f.raw, "SELECT count(*) FROM peer_grants"), 0)
	retired, err := f.db.RetireSpaceContact(ctx, f.contact, first.Epoch+1, time.Now())
	pairOK(t, err)
	pairEqual(t, retired, false)
	retired, err = f.db.RetireSpaceContact(ctx, f.contact, first.Epoch, time.Now())
	pairOK(t, err)
	pairEqual(t, retired, true)
	refused, err := f.db.SpaceRefused(ctx, f.contact)
	pairOK(t, err)
	pairEqual(t, refused, false)
	in.ConnectID[0]++
	in.PairingID[0]++
	_, err = f.db.InstallSpaceContact(ctx, in)
	pairOK(t, err)
	_, _, err = f.db.UnpairContact(ctx, unpairRequest(t, f, "operator-unpair"))
	pairOK(t, err)
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	in.Space = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	in.ConnectID[0]++
	in.PairingID[0]++
	_, err = f.db.InstallSpaceContact(ctx, in)
	pairEqual(t, errors.Is(err, store.ErrSpaceRefused), true)
}
