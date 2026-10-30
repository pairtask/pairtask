package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestRemoteRoutingAcceptanceExhaustedLocalIDsLeaveImportPending(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	routingRegister(t, db, "recipient")
	_, err := db.PeerMessages().Share(ctx, contact, []string{"recipient"}, messageAt)
	pairOK(t, err)
	imp := (routingScope{db, contact}).stage(t, "all-candidates-taken", "recipient")
	for i := 0; i <= 64; i++ {
		id := imp.LocalID
		if i > 0 {
			id = fmt.Sprintf("%s-%d", id, i)
		}
		at := messageAt
		msg := protocol.Message{Type: protocol.TypeMsg, MessageID: id, Seq: uint64(i + 1), TS: &at, From: "local", To: "local-recipient", Payload: []byte(`{"text":"existing local message"}`)}
		pairOK(t, db.PersistMessage(ctx, msg, "local-session", nil))
	}
	_, err = db.ImportRemote(ctx, imp, 100, messageAt)
	if !errors.Is(err, store.ErrLocalIDsTaken) {
		t.Fatalf("exhausted ID candidates: %v", err)
	}
	pending, err := db.PendingImports(ctx, 10)
	pairOK(t, err)
	pairEqual(t, len(pending), 1)
	pairEqual(t, pending[0].LocalID, imp.LocalID)
	got, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "recipient", SessionID: "new"}, 12)
	pairOK(t, err)
	pairEqual(t, len(got), 0)
}

func TestRemoteRoutingAcceptanceCollectionRetainsUnacknowledgedImportMapping(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	routingRegister(t, db, "recipient")
	s := db.PeerMessages()
	_, err := s.Share(ctx, contact, []string{"recipient"}, messageAt)
	pairOK(t, err)
	imp := (routingScope{db, contact}).stage(t, "retain-until-ack", "recipient")
	result, err := db.ImportRemote(ctx, imp, 1, messageAt)
	pairOK(t, err)
	later := messageAt.Add(365 * 24 * time.Hour)
	pairOK(t, s.Collect(ctx, later))
	pairEqual(t, len(messagePage(t, s, contact)), 0)
	// The peer row is gone, but the unacknowledged local inbox still needs
	// its mapping for a later agent receipt and trusted remote provenance.
	settled, err := db.ImportRemote(ctx, imp, 2, later)
	pairOK(t, err)
	pairEqual(t, settled.State, store.ImportImported)
	owner := protocol.Recipient{AgentID: "recipient", SessionID: "new"}
	got, err := db.ClaimPendingFor(ctx, owner, 12)
	pairOK(t, err)
	pairEqual(t, len(got), 1)
	if got[0].Remote == nil {
		t.Fatal("collection lost remote provenance")
	}
	for _, stage := range []string{protocol.StageAdapterReceived, protocol.StageAgentAcknowledged} {
		_, err := db.Acknowledge(ctx, store.Acknowledgment{MessageID: result.Msg.MessageID, Recipient: owner, Stage: stage, ProtocolVersion: 12})
		pairOK(t, err)
	}
	pairOK(t, s.QueueAgentRead(ctx, result.Msg.MessageID, "recipient", later))
	reads, err := s.PendingAgentReads(ctx, contact, 10)
	pairOK(t, err)
	pairEqual(t, len(reads), 1)
	pairOK(t, s.AgentReadConfirmed(ctx, contact, reads[0]))
	pairOK(t, s.Collect(ctx, later))
	_, err = db.ImportRemote(ctx, imp, 3, later)
	if !errors.Is(err, store.ErrNoImport) {
		t.Fatalf("settled mapping never collected: %v", err)
	}
	// Forgetting the mapping cannot make the old wire sequence importable.
	m := messageWire(imp.RemoteID, 1)
	m.From, m.To = "remote-sender", []string{"recipient"}
	a, err := s.Accept(ctx, contact, m, later)
	pairOK(t, err)
	pairEqual(t, a.Stored, false)
}

func TestRemoteRoutingAcceptancePendingAgentReceiptExpires(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	routingRegister(t, db, "recipient")
	s := db.PeerMessages()
	_, err := s.Share(ctx, contact, []string{"recipient"}, messageAt)
	pairOK(t, err)
	imp := (routingScope{db, contact}).stage(t, "expire-receipt", "recipient")
	result, err := db.ImportRemote(ctx, imp, 1, messageAt)
	pairOK(t, err)
	pairOK(t, s.QueueAgentRead(ctx, result.Msg.MessageID, "recipient", messageAt))
	pairOK(t, s.Collect(ctx, messageAt.Add(messaging.ReceiptAge+time.Second)))
	reads, err := s.PendingAgentReads(ctx, contact, 10)
	pairOK(t, err)
	pairEqual(t, len(reads), 0)
}
