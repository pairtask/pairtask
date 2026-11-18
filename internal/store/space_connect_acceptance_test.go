package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

const installSpaceID = "0123456789abcdef0123456789abcdef"

func spaceInstallFixture(key, role string) peer.SpaceInstall {
	in := peer.SpaceInstall{
		Contact: peer.Contact{IdentityKey: key, Name: "Verified Alice", EstablishedAt: messageAt},
		Space:   installSpaceID, ConnectID: [16]byte{1, 2, 3}, PairingID: sha256.Sum256([]byte("space pairing one")), Role: role,
	}
	if role == "joiner" {
		in.Contact.Joined = true
		in.Contact.Destination = "alice.example:7445"
		in.Contact.DestinationAt = messageAt
	} else {
		in.Contact.Invited = true
	}
	return in
}
func spaceInstallOperation(in peer.SpaceInstall) string {
	return "space:" + in.Space + ":" + hex.EncodeToString(in.ConnectID[:])
}
func spaceInstall(t *testing.T, f *operatorContactFixture, in peer.SpaceInstall) peer.InstallResult {
	t.Helper()
	result, err := f.db.InstallSpaceContact(context.Background(), in)
	pairOK(t, err)
	return result
}
func spaceAuthority(t *testing.T, f *operatorContactFixture) map[string][]string {
	t.Helper()
	rows := f.installAuthority(t)
	for _, table := range []string{"pairing_exchanges", "peer_grants"} {
		rows[table] = epochRows(t, f.raw, table, epochColumns(t, f.raw, table))
	}
	return rows
}

func TestSpaceInstallAcceptanceCreatesConfirmedEpochWithoutGrants(t *testing.T) {
	for _, role := range []string{"joiner", "inviter"} {
		t.Run(role, func(t *testing.T) {
			f := installFixture(t)
			in := spaceInstallFixture(f.contact, role)
			result := spaceInstall(t, f, in)
			pairEqual(t, result.Changed, true)
			pairEqual(t, result.Epoch, int64(1))
			pairEqual(t, bytes.Equal(result.PairingID, in.PairingID[:]), true)
			ledger := f.exchange(t, spaceInstallOperation(in))
			assertSpaceLedger(t, ledger, in)
			got, ok, err := f.db.Contact(context.Background(), f.contact)
			pairOK(t, err)
			pairEqual(t, ok, true)
			pairEqual(t, got.IdentityKey, in.Contact.IdentityKey)
			pairEqual(t, got.Name, in.Contact.Name)
			pairEqual(t, got.Joined, in.Contact.Joined)
			pairEqual(t, got.Invited, in.Contact.Invited)
			pairEqual(t, got.Destination, in.Contact.Destination)
			confirmed, err := f.db.PeerMessages().Confirmed(context.Background(), f.contact)
			pairOK(t, err)
			pairEqual(t, confirmed, true)
			pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_grants`), 0)
		})
	}
}
func assertSpaceLedger(t *testing.T, got peer.ExchangeRecord, in peer.SpaceInstall) {
	t.Helper()
	pairEqual(t, got.IdentityKey, in.Contact.IdentityKey)
	pairEqual(t, got.Role, in.Role)
	pairEqual(t, got.Kind, peer.KindPair)
	pairEqual(t, got.Mode, peer.ModeEpoch)
	pairEqual(t, got.State, peer.ExchangeInstalled)
	pairEqual(t, got.Decision, peer.PairingAccepted)
	pairEqual(t, got.InstalledEpoch, int64(1))
}

func TestSpaceInstallAcceptanceReplaySurvivesRestartWithoutWrites(t *testing.T) {
	f := installFixture(t)
	in := spaceInstallFixture(f.contact, "joiner")
	first := spaceInstall(t, f, in)
	before := spaceAuthority(t, f)
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	again := spaceInstall(t, f, in)
	pairEqual(t, again.Changed, false)
	pairEqual(t, again.Epoch, first.Epoch)
	pairEqual(t, bytes.Equal(again.PairingID, first.PairingID), true)
	pairEqual(t, reflect.DeepEqual(before, spaceAuthority(t, f)), true)
}

func TestSpaceInstallAcceptanceTupleConflictChangesNothing(t *testing.T) {
	for name, change := range map[string]func(*peer.SpaceInstall){
		"peer":       func(in *peer.SpaceInstall) { in.Contact.IdentityKey = joinKey(12) },
		"pairing-id": func(in *peer.SpaceInstall) { in.PairingID[0] ^= 1 },
		"role": func(in *peer.SpaceInstall) {
			in.Role = "inviter"
			in.Contact.Joined = false
			in.Contact.Invited = true
			in.Contact.Destination = ""
		},
		"space":   func(in *peer.SpaceInstall) { in.Space = strings.Repeat("b", 32) },
		"attempt": func(in *peer.SpaceInstall) { in.ConnectID[0] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			f := installFixture(t)
			in := spaceInstallFixture(f.contact, "joiner")
			spaceInstall(t, f, in)
			before := spaceAuthority(t, f)
			change(&in)
			_, err := f.db.InstallSpaceContact(context.Background(), in)
			pairEqual(t, errors.Is(err, peer.ErrSpaceConflict), true)
			pairEqual(t, reflect.DeepEqual(before, spaceAuthority(t, f)), true)
		})
	}
}

func TestSpaceInstallAcceptanceCannotReplaceCodePairingOrGrants(t *testing.T) {
	f := installFixture(t)
	f.installedEpoch(t, 101)
	_, err := f.db.PeerMessages().Share(context.Background(), f.contact, []string{"existing-agent"}, messageAt)
	pairOK(t, err)
	before := spaceAuthority(t, f)
	_, err = f.db.InstallSpaceContact(context.Background(), spaceInstallFixture(f.contact, "joiner"))
	pairEqual(t, errors.Is(err, peer.ErrSpaceConflict), true)
	pairEqual(t, reflect.DeepEqual(before, spaceAuthority(t, f)), true)
}

func TestSpaceInstallAcceptanceRetiredReplayCannotReactivate(t *testing.T) {
	f := installFixture(t)
	in := spaceInstallFixture(f.contact, "joiner")
	spaceInstall(t, f, in)
	_, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "space-unpair"))
	pairOK(t, err)
	before := spaceAuthority(t, f)
	again := spaceInstall(t, f, in)
	pairEqual(t, again.Changed, false)
	pairEqual(t, again.Exchange.State, peer.ExchangeRetired)
	pairEqual(t, reflect.DeepEqual(before, spaceAuthority(t, f)), true)
	confirmed, err := f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, false)
}

func TestSpaceInstallAcceptanceFreshAttemptAfterUnpairInstallsNewEpoch(t *testing.T) {
	f := installFixture(t)
	in := spaceInstallFixture(f.contact, "joiner")
	spaceInstall(t, f, in)
	_, err := f.db.PeerMessages().Share(context.Background(), f.contact, []string{"retired-agent"}, messageAt)
	pairOK(t, err)
	_, _, err = f.db.UnpairContact(context.Background(), unpairRequest(t, f, "space-unpair-new"))
	pairOK(t, err)
	in.ConnectID[0]++
	in.PairingID = sha256.Sum256([]byte("space pairing two"))
	next := spaceInstall(t, f, in)
	pairEqual(t, next.Changed, true)
	pairEqual(t, next.Epoch, int64(2))
	pairEqual(t, bytes.Equal(next.PairingID, in.PairingID[:]), true)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_grants WHERE pairing_epoch=2`), 0)
}

func TestSpaceAttemptAcceptanceDurableAndScopedUntilEnded(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "attempts.db")
	db := pairSQLite(t, path)
	key := joinKey(11)
	first, second := [16]byte{1}, [16]byte{2}
	got, err := db.SpaceAttempt(ctx, installSpaceID, key, first)
	pairOK(t, err)
	pairEqual(t, got, first)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	got, err = db.SpaceAttempt(ctx, installSpaceID, key, second)
	pairOK(t, err)
	pairEqual(t, got, first)
	for _, target := range []struct{ space, key string }{{strings.Repeat("b", 32), key}, {installSpaceID, joinKey(12)}} {
		got, err = db.SpaceAttempt(ctx, target.space, target.key, second)
		pairOK(t, err)
		pairEqual(t, got, second)
	}
	pairOK(t, db.EndSpaceAttempt(ctx, installSpaceID, key))
	pairOK(t, db.EndSpaceAttempt(ctx, installSpaceID, key))
	got, err = db.SpaceAttempt(ctx, installSpaceID, key, second)
	pairOK(t, err)
	pairEqual(t, got, second)
	contacts, err := db.Contacts(ctx)
	pairOK(t, err)
	pairEqual(t, len(contacts), 0)
}
