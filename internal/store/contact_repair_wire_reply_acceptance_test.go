package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func namespaceReply(parent, id string, seq uint64) store.RemoteReply {
	return store.RemoteReply{Msg: protocol.Message{Type: protocol.TypeMsg, MessageID: id, Seq: seq, TS: &messageAt, From: "recipient", To: "remote-sender", InReplyTo: parent, Payload: []byte(`{"text":"reply"}`)}, SenderSession: "session-recipient", Parent: parent, Text: "reply", Now: messageAt}
}

func TestContactRepairWireAcceptanceOldImportedReplyIsRefusedWithoutMutation(t *testing.T) {
	f := namespaceFixture(t)
	old := f.wireImported(t, "old-parent", "old body", 1)
	namespaceRepair(t, f)
	namespaceShare(t, f, "recipient")
	before := wireMutationSnapshot(t, f)
	_, err := f.db.QueueRemoteReply(context.Background(), namespaceReply(old.Msg.MessageID, "stale-reply", 10))
	if !errors.Is(err, store.ErrUnpaired) {
		t.Errorf("old parent reply must be ErrUnpaired, got %v", err)
	}
	if !reflect.DeepEqual(before, wireMutationSnapshot(t, f)) {
		t.Fatal("stale parent reply changed local/outbox/audit/sequence state")
	}
}

func TestContactRepairWireAcceptanceCurrentImportedParentReplyQueues(t *testing.T) {
	f := namespaceFixture(t)
	namespaceRepair(t, f)
	namespaceShare(t, f, "recipient")
	current := f.wireImported(t, "current-parent", "new body", 2)
	request := namespaceReply(current.Msg.MessageID, "new-reply", 11)
	first, err := f.db.QueueRemoteReply(context.Background(), request)
	pairOK(t, err)
	pairEqual(t, first.Repeated, false)
	repeated, err := f.db.QueueRemoteReply(context.Background(), request)
	pairOK(t, err)
	pairEqual(t, repeated.Repeated, true)
	pairEqual(t, first.Row.ID, repeated.Row.ID)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_messages WHERE pairing_epoch=2 AND direction='out' AND message_id='new-reply'`), 1)
}

func TestContactRepairWireAcceptanceOldPairedAssignmentCannotSendIntoNewPairing(t *testing.T) {
	p := pairedStore(t)
	f := workspaceStoreFixture{db: p.db, raw: p.raw, key: p.contact}
	namespaceRepair(t, f)
	before := wireMutationSnapshot(t, f)
	_, err := p.db.PairedReply(context.Background(), p.reply("old-assignment-reply", p.notice.MessageID))
	if !errors.Is(err, store.ErrUnpaired) {
		t.Errorf("old paired assignment must be ErrUnpaired, got %v", err)
	}
	if !reflect.DeepEqual(before, wireMutationSnapshot(t, f)) {
		t.Fatal("retired paired-thread reply changed local/outbox/audit state")
	}
}

func TestContactRepairWireAcceptanceNewPairedThreadAndReplyCarryNewEpoch(t *testing.T) {
	p := pairedStore(t)
	f := workspaceStoreFixture{db: p.db, raw: p.raw, key: p.contact}
	namespaceRepair(t, f)
	_, err := p.db.OperatorContactSend(context.Background(), store.ContactSend{OperationID: "new-paired-root", Contact: p.contact, Text: "new question", To: []string{"remote-a"}, Here: []string{"home-a"}, AuditID: p.audit(t, contactAuditInput{"send", "new-paired-root"}), Now: p.now.Add(3 * time.Second)})
	pairOK(t, err)
	var root string
	var epoch int64
	pairOK(t, p.raw.QueryRow(`SELECT root_local_id,pairing_epoch FROM paired_threads WHERE contact=? AND peer_id='new-paired-root' LIMIT 1`, p.contact).Scan(&root, &epoch))
	pairEqual(t, epoch, int64(2))
	reply := p.reply("new-paired-reply", root)
	reply.Now = p.now.Add(4 * time.Second)
	result, err := p.db.PairedReply(context.Background(), reply)
	pairOK(t, err)
	pairEqual(t, result.PeerDelivery.State, protocol.PeerQueued)
	pairEqual(t, epochCount(t, p.raw, `SELECT count(*) FROM paired_reply_outcomes WHERE message_id='new-paired-reply' AND pairing_epoch=2`), 1)
	pairEqual(t, epochCount(t, p.raw, `SELECT count(*) FROM peer_messages WHERE message_id='new-paired-reply' AND pairing_epoch=2 AND direction='out'`), 1)
}
