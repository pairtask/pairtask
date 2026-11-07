package store_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestControlStoreAcceptanceRemoteUnpairIsAtomicAndTerminal(t *testing.T) {
	f, c, id := controlStoreFixture(t)
	ctx := context.Background()
	_, err := f.db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: f.contact, ID: "remote-unpair-queued", Text: "body", Now: messageAt})
	pairOK(t, err)
	_, err = f.raw.Exec(`INSERT INTO peer_grants(contact,pairing_epoch,agent,granted_at) VALUES (?,1,'evidence',?)`, f.contact, messageAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
	pairOK(t, err)
	beforeMark, err := f.db.PairingMark(ctx)
	pairOK(t, err)
	var beforeGen int64
	err = f.raw.QueryRow(`SELECT generation FROM peer_contacts WHERE identity_key=?`, f.contact).Scan(&beforeGen)
	pairOK(t, err)
	a := controlAccept(t, c, messaging.ContactState{PairingID: id, Gen: 31, State: 3})
	pairEqual(t, a.Close, true)
	var gen, epoch int64
	err = f.raw.QueryRow(`SELECT generation,pairing_epoch FROM peer_contacts WHERE identity_key=?`, f.contact).Scan(&gen, &epoch)
	pairOK(t, err)
	pairEqual(t, gen, beforeGen+1)
	pairEqual(t, epoch, int64(1))
	afterMark, err := f.db.PairingMark(ctx)
	pairOK(t, err)
	pairEqual(t, afterMark, beforeMark+1)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_grants WHERE agent='evidence'`), 1)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_messages WHERE message_id='remote-unpair-queued' AND state='expired' AND reason='unpaired_by_them'`), 1)
	_, err = f.db.PeerMessages().Admit(ctx, f.contact)
	pairEqual(t, errors.Is(err, store.ErrUnpairedByThem), true)
	_, err = f.db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: f.contact, ID: "after-remote-unpair", Text: "no", Now: messageAt})
	pairEqual(t, errors.Is(err, store.ErrUnpairedByThem), true)
	fresh, err := f.db.PeerMessages().AdmitControl(ctx, f.contact)
	pairOK(t, err)
	before := f.installAuthority(t)
	controlAccept(t, fresh, messaging.ContactState{PairingID: id, Gen: 40, State: 2})
	duplicate := controlAccept(t, fresh, messaging.ContactState{PairingID: id, Gen: 31, State: 3})
	pairEqual(t, duplicate.Close, true)
	if !reflect.DeepEqual(before, f.installAuthority(t)) {
		t.Fatal("terminal ID accepted a later state or changed on duplicate")
	}
}
func TestControlStoreAcceptanceRemoteUnpairExhaustionRollsBack(t *testing.T) {
	for _, component := range []string{"generation", "mark"} {
		t.Run(component, func(t *testing.T) {
			f, _, id := controlStoreFixture(t)
			if component == "generation" {
				_, err := f.raw.Exec(`UPDATE peer_contacts SET generation=? WHERE identity_key=?`, int64(math.MaxInt64), f.contact)
				pairOK(t, err)
			} else {
				_, err := f.raw.Exec(`UPDATE pairing_marks SET mark=? WHERE id=1`, int64(math.MaxInt64))
				pairOK(t, err)
			}
			c, err := f.db.PeerMessages().AdmitControl(context.Background(), f.contact)
			pairOK(t, err)
			before := f.installAuthority(t)
			_, err = c.AcceptNotice(context.Background(), messaging.ContactState{PairingID: id, Gen: 1, State: 3})
			if err == nil {
				t.Fatal("authority counter wrapped")
			}
			if !reflect.DeepEqual(before, f.installAuthority(t)) {
				t.Fatal("exhausted remote unpair partly committed")
			}
		})
	}
}
func TestControlStoreAcceptanceNoticeAckRequiresExactIDAndGeneration(t *testing.T) {
	f, _, id := controlStoreFixture(t)
	out, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "control-ack"))
	pairOK(t, err)
	c, err := f.db.PeerMessages().AdmitControl(context.Background(), f.contact)
	pairOK(t, err)
	n, err := c.PendingNotice(context.Background())
	pairOK(t, err)
	if n == nil {
		t.Fatal("local unpair did not queue a notice")
	}
	pairEqual(t, n.Gen, uint64(out.Generation))
	pairEqual(t, n.State, byte(3))
	for _, ack := range []messaging.ContactStateStored{{PairingID: nil, Gen: n.Gen}, {PairingID: id, Gen: n.Gen + 1}} {
		err = c.NoticeStored(context.Background(), ack)
		pairEqual(t, errors.Is(err, messaging.ErrProtocol), true)
	}
	pending, err := c.PendingNotice(context.Background())
	pairOK(t, err)
	if pending == nil {
		t.Fatal("wrong ACK consumed pending notice")
	}
	exact := messaging.ContactStateStored{PairingID: id, Gen: n.Gen}
	pairOK(t, c.NoticeStored(context.Background(), exact))
	pairOK(t, c.NoticeStored(context.Background(), exact))
	pending, err = c.PendingNotice(context.Background())
	pairOK(t, err)
	pairEqual(t, pending == nil, true)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM contact_notices_out WHERE state='delivered'`), 1)
}
