package store_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestControlStoreAcceptanceNoticeOrderingAndExactDuplicate(t *testing.T) {
	f, c, id := controlStoreFixture(t)
	for _, step := range []struct {
		gen     uint64
		state   byte
		want    string
		wantGen int64
	}{
		{5, 1, "paused", 5}, {6, 2, "resumed", 6}, {4, 1, "resumed", 6}, {6, 1, "resumed", 6}, {6, 2, "resumed", 6},
	} {
		n := messaging.ContactState{PairingID: id, Gen: step.gen, State: step.state}
		a := controlAccept(t, c, n)
		pairEqual(t, a.Ack.Gen, step.gen)
		state, gen := controlPeerState(t, f)
		pairEqual(t, state, step.want)
		pairEqual(t, gen, step.wantGen)
	}
}
func TestControlStoreAcceptanceWrongPairingAndUnsignedOverflowChangeNothing(t *testing.T) {
	f, c, id := controlStoreFixture(t)
	before := f.installAuthority(t)
	_, err := c.AcceptNotice(context.Background(), messaging.ContactState{PairingID: bytes.Repeat([]byte{2}, 32), Gen: 1, State: 1})
	pairEqual(t, errors.Is(err, messaging.ErrOtherPairing), true)
	_, err = c.AcceptNotice(context.Background(), messaging.ContactState{PairingID: id, Gen: uint64(math.MaxInt64) + 1, State: 1})
	pairEqual(t, errors.Is(err, messaging.ErrProtocol), true)
	if !reflect.DeepEqual(f.installAuthority(t), before) {
		t.Fatal("invalid notice changed authority")
	}
	controlAccept(t, c, messaging.ContactState{PairingID: id, Gen: math.MaxInt64, State: 1})
	state, gen := controlPeerState(t, f)
	pairEqual(t, state, "paused")
	pairEqual(t, gen, int64(math.MaxInt64))
}
func TestControlStoreAcceptanceEveryReadAndWriteIsFenced(t *testing.T) {
	for _, component := range []string{"generation", "pairing_epoch", "pairing_id"} {
		t.Run(component, func(t *testing.T) {
			f, c, id := controlStoreFixture(t)
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
			_, err := c.PairingID(context.Background())
			pairEqual(t, errors.Is(err, store.ErrFenced), true)
			_, err = c.PendingNotice(context.Background())
			pairEqual(t, errors.Is(err, store.ErrFenced), true)
			_, err = c.AcceptNotice(context.Background(), messaging.ContactState{PairingID: id, Gen: 1, State: 1})
			pairEqual(t, errors.Is(err, store.ErrFenced), true)
			err = c.NoticeStored(context.Background(), messaging.ContactStateStored{PairingID: id, Gen: 1})
			pairEqual(t, errors.Is(err, store.ErrFenced), true)
		})
	}
}
func TestControlStoreAcceptanceAdmitsRetainedButNeverRefreshesBoundStore(t *testing.T) {
	f, c, _ := controlStoreFixture(t)
	full, err := f.db.PeerMessages().Admit(context.Background(), f.contact)
	pairOK(t, err)
	_, err = full.AdmitControl(context.Background(), f.contact)
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
	_, _, err = f.db.UnpairContact(context.Background(), unpairRequest(t, f, "control-retained"))
	pairOK(t, err)
	_, err = c.PairingID(context.Background())
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
	fresh, err := f.db.PeerMessages().AdmitControl(context.Background(), f.contact)
	pairOK(t, err)
	_, err = fresh.PairingID(context.Background())
	pairOK(t, err)
	_, err = f.db.PeerMessages().Admit(context.Background(), f.contact)
	pairEqual(t, errors.Is(err, store.ErrUnpaired), true)
}

func TestControlStoreAcceptanceLegacyPausedAndUnknownAdmission(t *testing.T) {
	f := newOperatorContactFixture(t)
	c, err := f.db.PeerMessages().AdmitControl(context.Background(), f.contact)
	pairOK(t, err)
	id, err := c.PairingID(context.Background())
	pairOK(t, err)
	pairEqual(t, len(id), 0)
	pauseApply(t, f, pauseRequest(t, f, "control-paused", true))
	_, err = c.PendingNotice(context.Background())
	pairEqual(t, errors.Is(err, store.ErrFenced), true)
	fresh, err := f.db.PeerMessages().AdmitControl(context.Background(), f.contact)
	pairOK(t, err)
	_, err = fresh.PairingID(context.Background())
	pairOK(t, err)
	_, err = f.db.PeerMessages().AdmitControl(context.Background(), joinKey(99))
	pairEqual(t, errors.Is(err, messaging.ErrUnknownContact), true)
}
