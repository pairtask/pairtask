package store_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestPairingInstallAcceptanceActiveRepairKeepsAuthorityUntilPinnedInstall(t *testing.T) {
	f := installFixture(t)
	old := f.installedEpoch(t, 101)
	oldBound, err := f.db.PeerMessages().Admit(context.Background(), f.contact)
	pairOK(t, err)
	oldID := f.exchange(t, old.OperationID)
	repair := f.epochOutcome(t, 102)
	repair.Repair = f.contact
	_, err = f.db.Commit(context.Background(), repair)
	pairOK(t, err)
	candidate := f.exchange(t, repair.OperationID)
	pairEqual(t, candidate.Kind, peer.KindRepair)
	pairEqual(t, candidate.State, "open")
	pairEqual(t, f.exchange(t, old.OperationID).State, "installed")
	_, err = oldBound.Offer(context.Background(), f.contact, messageAt, messaging.SessionLimit)
	pairOK(t, err)
	result := f.deliverEpoch(t, repair)
	pairEqual(t, result.Changed, true)
	pairEqual(t, result.Epoch, int64(2))
	retired := f.exchange(t, old.OperationID)
	pairEqual(t, retired.State, "retired")
	pairEqual(t, retired.Reason, "replaced")
	pairEqual(t, retired.InstalledEpoch, oldID.InstalledEpoch)
	_, err = oldBound.Offer(context.Background(), f.contact, messageAt, messaging.SessionLimit)
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
}

func TestPairingInstallAcceptanceUnpairedReplayCannotReactivateContact(t *testing.T) {
	f := installFixture(t)
	o := f.installedEpoch(t, 101)
	_, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "retire-installed"))
	pairOK(t, err)
	before := f.installAuthority(t)
	retired := f.exchange(t, o.OperationID)
	result, err := f.db.DeliverExchange(context.Background(), peer.ExchangeDelivery{IdentityKey: f.contact, OperationID: o.OperationID, Decision: peer.PairingAccepted, At: messageAt})
	pairOK(t, err)
	pairEqual(t, result.Changed, false)
	pairEqual(t, result.Epoch, int64(1))
	pairEqual(t, result.Exchange.State, "retired")
	if !reflect.DeepEqual(retired, f.exchange(t, o.OperationID)) {
		t.Fatal("retired replay rewrote ledger")
	}
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("retired replay revived authority or moved a counter")
	}
	confirmed, err := f.db.PeerMessages().Confirmed(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, confirmed, false)
}

func TestPairingInstallAcceptanceFreshRepairStartsWithNoOldAuthority(t *testing.T) {
	f := installFixture(t)
	old := f.installedEpoch(t, 101)
	firstID := bytes.Clone(peer.PairingID(old.TranscriptID))
	_, err := f.db.PeerMessages().Share(context.Background(), f.contact, []string{"old-agent"}, messageAt)
	pairOK(t, err)
	pairOK(t, f.db.PeerMessages().SetPeerDirectory(context.Background(), f.contact, []string{"remote-old-agent"}, messageAt))
	_, err = f.db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.contact, ID: "retired-outgoing", Text: "old body", Now: messageAt})
	pairOK(t, err)
	_, err = f.raw.Exec(`UPDATE peer_contacts SET peer_state='paused' WHERE identity_key=?`, f.contact)
	pairOK(t, err)
	_, _, err = f.db.UnpairContact(context.Background(), unpairRequest(t, f, "fresh-unpair"))
	pairOK(t, err)
	next := f.epochOutcome(t, 102)
	request, err := f.db.Commit(context.Background(), next)
	pairOK(t, err)
	pairEqual(t, request.State, peer.PairingPending)
	result := f.deliverEpoch(t, next)
	pairEqual(t, result.Epoch, int64(2))
	pairEqual(t, result.Changed, true)
	if bytes.Equal(firstID, result.PairingID) {
		t.Fatal("fresh transcript reused the retired pairing ID")
	}
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_grants WHERE pairing_epoch=1`), 1)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_grants WHERE pairing_epoch=2`), 0)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_messages WHERE pairing_epoch=1 AND message_id='retired-outgoing' AND state='expired'`), 1)
	var directoryAt, peerState string
	pairOK(t, f.raw.QueryRow(`SELECT COALESCE(directory_at,''),peer_state FROM peer_contacts WHERE identity_key=?`, f.contact).Scan(&directoryAt, &peerState))
	pairEqual(t, directoryAt, "")
	pairEqual(t, peerState, "")
	agents, err := f.db.PeerMessages().PeerDirectory(context.Background(), f.contact)
	pairOK(t, err)
	pairEqual(t, len(agents), 0)
	row, err := f.db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.contact, ID: "fresh-outgoing", Text: "new body", Now: messageAt})
	pairOK(t, err)
	pairEqual(t, row.N, uint64(1))
	pairEqual(t, row.PairingEpoch, int64(2))
}

func TestPairingInstallAcceptanceAddressMoveNeverReplacesEpochAuthority(t *testing.T) {
	f := installFixture(t)
	old := f.installedEpoch(t, 101)
	move := f.epochOutcome(t, 102)
	before := f.installAuthority(t)
	_, err := f.db.Commit(context.Background(), move)
	pairOK(t, err)
	x := f.exchange(t, move.OperationID)
	pairEqual(t, x.Kind, peer.KindAddressMove)
	pairEqual(t, x.State, "refused")
	pairEqual(t, x.Reason, "address_move")
	// The immutable outcome may be added; authority and request mirrors stay put.
	after := f.installAuthority(t)
	delete(before, "peer_pairing_outcomes")
	delete(after, "peer_pairing_outcomes")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("undeclared address move changed installed authority")
	}
	result, err := f.db.DeliverExchange(context.Background(), peer.ExchangeDelivery{IdentityKey: f.contact, OperationID: move.OperationID, Decision: peer.PairingAccepted, At: messageAt})
	pairOK(t, err)
	pairEqual(t, result.Changed, false)
	pairEqual(t, result.Epoch, int64(1))
	pairEqual(t, bytes.Equal(result.PairingID, peer.PairingID(old.TranscriptID)), true)
	pairEqual(t, f.exchange(t, old.OperationID).State, "installed")
}

func TestPairingInstallAcceptanceWrongOperationIdentityCannotDeliver(t *testing.T) {
	f := installFixture(t)
	o := f.epochOutcome(t, 101)
	_, err := f.db.Commit(context.Background(), o)
	pairOK(t, err)
	_, err = f.db.Accept(context.Background(), f.contact)
	pairOK(t, err)
	before := f.installAuthority(t)
	_, err = f.db.DeliverExchange(context.Background(), peer.ExchangeDelivery{IdentityKey: joinKey(999), OperationID: o.OperationID, Decision: peer.PairingAccepted, At: messageAt})
	if err == nil {
		t.Fatal("pinned operation delivered under another identity")
	}
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("wrong identity delivery changed authority")
	}
}

func TestPairingInstallAcceptanceHistoricalReplayCannotRetargetFreshPairing(t *testing.T) {
	f := installFixture(t)
	old := f.installedEpoch(t, 101)
	_, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "history-before-fresh"))
	pairOK(t, err)
	fresh := f.installedEpoch(t, 102)
	before := f.installAuthority(t)
	current := f.exchange(t, fresh.OperationID)
	replay, err := f.db.DeliverExchange(context.Background(), peer.ExchangeDelivery{IdentityKey: f.contact, OperationID: old.OperationID, Decision: peer.PairingAccepted, At: messageAt})
	pairOK(t, err)
	pairEqual(t, replay.Changed, false)
	pairEqual(t, replay.Epoch, int64(1))
	pairEqual(t, bytes.Equal(replay.PairingID, peer.PairingID(old.TranscriptID)), true)
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("historical install replay changed the fresh pairing")
	}
	if !reflect.DeepEqual(current, f.exchange(t, fresh.OperationID)) {
		t.Fatal("historical replay changed the installed fresh ledger")
	}
}
