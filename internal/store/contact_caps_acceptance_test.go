package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestContactCapsAcceptanceActiveBoundaryAndUnpairFreesSlot(t *testing.T) {
	pairEqual(t, peer.MaxActiveContacts, 1024)
	f := installFixture(t)
	f.installedEpoch(t, 101)
	seedContactCaps(t, f, 1023, 0)
	candidate := *f
	candidate.contact = joinKey(9001)
	err := capTryInstall(t, &candidate, 102)
	pairEqual(t, errors.Is(err, peer.ErrIdentityStorage), true)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts WHERE unpaired_at=''`), 1024)
	// Unpairing remains allowed at full capacity and makes exactly one slot.
	_, _, err = f.db.UnpairContact(context.Background(), unpairRequest(t, f, "cap-free-slot"))
	pairOK(t, err)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts WHERE unpaired_at=''`), 1023)
	// A different new identity can use the slot.
	candidate.contact = joinKey(9004)
	pairOK(t, capTryInstall(t, &candidate, 103))
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts WHERE unpaired_at=''`), 1024)
}
func TestContactCapsAcceptanceRetainedBoundaryRejectsNewButReusesOldRow(t *testing.T) {
	pairEqual(t, peer.MaxRetainedIdentities, 2048)
	f := installFixture(t)
	f.installedEpoch(t, 101)
	seedContactCaps(t, f, 0, 2047)
	candidate := *f
	candidate.contact = joinKey(9002)
	err := capTryInstall(t, &candidate, 102)
	pairEqual(t, errors.Is(err, peer.ErrIdentityStorage), true)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts`), 2048)
	_, _, err = f.db.UnpairContact(context.Background(), unpairRequest(t, f, "cap-reuse-row"))
	pairOK(t, err)
	pairOK(t, capTryInstall(t, f, 103))
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts`), 2048)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts WHERE unpaired_at=''`), 1)
}
func TestContactCapsAcceptanceRetiredRowsDoNotConsumeActiveSlots(t *testing.T) {
	f := installFixture(t)
	f.installedEpoch(t, 101)
	seedContactCaps(t, f, 0, 1024)
	candidate := *f
	candidate.contact = joinKey(9003)
	pairOK(t, capTryInstall(t, &candidate, 102))
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts WHERE unpaired_at=''`), 2)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts`), 1026)
}
