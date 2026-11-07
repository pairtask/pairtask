package store_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

type pairingVersionStore interface {
	PairingID() []byte
	MinVersion() byte
	SawVersion(context.Context, byte) error
}

func pairingVersionBound(t *testing.T, f *operatorContactFixture) pairingVersionStore {
	t.Helper()
	bound, err := f.db.PeerMessages().Admit(context.Background(), f.contact)
	pairOK(t, err)
	v, ok := any(bound).(pairingVersionStore)
	if !ok {
		t.Fatal("admitted PeerMessages lacks pairing version/ID API")
	}
	return v
}
func pairingVersionValue(t *testing.T, f *operatorContactFixture) int {
	t.Helper()
	var version int
	err := f.raw.QueryRow(`SELECT peer_version FROM peer_contacts WHERE identity_key=?`, f.contact).Scan(&version)
	pairOK(t, err)
	return version
}
func TestPairingVersionStoreAcceptanceLegacySnapshotAndPersistence(t *testing.T) {
	f := newOperatorContactFixture(t)
	bound := pairingVersionBound(t, f)
	pairEqual(t, len(bound.PairingID()), 0)
	pairEqual(t, bound.MinVersion(), byte(1))
	pairOK(t, bound.SawVersion(context.Background(), 8))
	pairEqual(t, pairingVersionValue(t, f), 8)
	pairOK(t, bound.SawVersion(context.Background(), 8))
	pairEqual(t, pairingVersionValue(t, f), 8)
	pairOK(t, f.db.Close())
	f.db = pairSQLite(t, f.path)
	pairEqual(t, pairingVersionValue(t, f), 8)
}
func TestPairingVersionStoreAcceptanceInstalledSnapshotCopiesID(t *testing.T) {
	f := installFixture(t)
	o := f.installedEpoch(t, 101)
	bound := pairingVersionBound(t, f)
	expected := peer.PairingID(o.TranscriptID)
	if !bytes.Equal(bound.PairingID(), expected) {
		t.Fatalf("ID %x want %x", bound.PairingID(), expected)
	}
	pairEqual(t, bound.MinVersion(), byte(9))
	escaped := bound.PairingID()
	escaped[0] ^= 0xff
	if !bytes.Equal(bound.PairingID(), expected) {
		t.Fatal("caller changed captured pairing ID")
	}
	pairOK(t, bound.SawVersion(context.Background(), 9))
	pairEqual(t, pairingVersionValue(t, f), 9)
}
func TestPairingVersionStoreAcceptanceFenceChecksEveryAuthorityComponent(t *testing.T) {
	for _, component := range []string{"generation", "pairing_epoch", "pairing_id"} {
		t.Run(component, func(t *testing.T) {
			f := installFixture(t)
			f.installedEpoch(t, 101)
			bound := pairingVersionBound(t, f)
			pairOK(t, bound.SawVersion(context.Background(), 9))
			switch component {
			case "generation":
				_, err := f.raw.Exec(`UPDATE peer_contacts SET generation=generation+1 WHERE identity_key=?`, f.contact)
				pairOK(t, err)
			case "pairing_epoch":
				_, err := f.raw.Exec(`UPDATE peer_contacts SET pairing_epoch=pairing_epoch+1 WHERE identity_key=?`, f.contact)
				pairOK(t, err)
			case "pairing_id":
				_, err := f.raw.Exec(`UPDATE peer_contacts SET pairing_id=? WHERE identity_key=?`, bytes.Repeat([]byte{7}, 32), f.contact)
				pairOK(t, err)
			}
			err := bound.SawVersion(context.Background(), 8)
			pairEqual(t, errors.Is(err, store.ErrFenced), true)
			_, err = any(bound).(*store.PeerMessages).Offer(context.Background(), f.contact, messageAt, messaging.SessionLimit)
			pairEqual(t, errors.Is(err, store.ErrFenced), true)
			pairEqual(t, pairingVersionValue(t, f), 9)
		})
	}
}
func TestPairingVersionStoreAcceptanceRepairKeepsOldSnapshotAndFencesWrite(t *testing.T) {
	f := installFixture(t)
	first := f.installedEpoch(t, 101)
	old := pairingVersionBound(t, f)
	pairOK(t, old.SawVersion(context.Background(), 9))
	next := f.epochOutcome(t, 102)
	next.Repair = f.contact
	_, err := f.db.Commit(context.Background(), next)
	pairOK(t, err)
	f.deliverEpoch(t, next)
	if !bytes.Equal(old.PairingID(), peer.PairingID(first.TranscriptID)) {
		t.Fatal("old store silently adopted replacement ID")
	}
	pairEqual(t, errors.Is(old.SawVersion(context.Background(), 8), store.ErrFenced), true)
	fresh := pairingVersionBound(t, f)
	if !bytes.Equal(fresh.PairingID(), peer.PairingID(next.TranscriptID)) {
		t.Fatal("new store did not capture replacement ID")
	}
	pairOK(t, fresh.SawVersion(context.Background(), 9))
	pairEqual(t, pairingVersionValue(t, f), 9)
}
