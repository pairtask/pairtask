package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// A deterministic broker boundary exercises the actual Node inbox and
// forwarder, including failures after one conversation has been collected.
func inboxDeliveryNode(t *testing.T, answer func(protocol.Message) protocol.Message) *Node {
	t.Helper()
	client, broker := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer broker.Close()
		decoder, encoder := json.NewDecoder(broker), json.NewEncoder(broker)
		for {
			var request protocol.Message
			if decoder.Decode(&request) != nil {
				return
			}
			if encoder.Encode(answer(request)) != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	return &Node{cfg: Config{Dir: t.TempDir()}, fwd: &forwarder{conn: client, in: bufio.NewScanner(client)}}
}

type inboxDeliveryBroker struct {
	failSecond bool
	backlog    int64
}

func (b inboxDeliveryBroker) answer(request protocol.Message) protocol.Message {
	if request.Type == protocol.TypeOperatorPairing {
		return protocol.Message{Type: protocol.TypeOperatorPairingResult, PairingResult: &protocol.PairingSnapshot{
			Contacts: []protocol.PairingContactView{{IdentityKey: "contact-a", Name: "A"}, {IdentityKey: "contact-b", Name: "B"}}}}
	}
	if request.ContactOp == protocol.ContactOpSync {
		return protocol.Message{Type: protocol.TypeError, Detail: "offline fixture"}
	}
	if b.failSecond && request.ContactKey == "contact-b" {
		return protocol.Message{Type: protocol.TypeError, Detail: "injected second-workspace read failure"}
	}
	return protocol.Message{Type: protocol.TypeOperatorContactResult, ContactPage: b.page(request.ContactBefore)}
}

func (b inboxDeliveryBroker) page(before int64) *protocol.ContactMessagePage {
	newest := b.backlog
	if before > 0 {
		newest = before - 1
	}
	page := &protocol.ContactMessagePage{}
	for cursor := newest; cursor > 0 && cursor > newest-50; cursor-- {
		page.Messages = append(page.Messages, protocol.ContactMessageView{
			ID: fmt.Sprintf("in-%d", cursor), Cursor: cursor, Direction: "in", Text: "incoming"})
	}
	page.More = newest > 50
	return page
}

func TestInboxDeliveryAcceptanceLaterWorkspaceFailureDoesNotConsumeEarlierMessages(t *testing.T) {
	broker := inboxDeliveryBroker{failSecond: true, backlog: 1}
	n := inboxDeliveryNode(t, broker.answer)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := n.inbox(ctx, InboxArgs{NewOnly: true, Consumer: "reader"}); err == nil {
		t.Fatal("fixture did not reach the later workspace failure")
	}
	// The failed call returned no messages. The next successful call must
	// still show the earlier workspace's message.
	rows, err := n.inbox(ctx, InboxArgs{From: "contact-a", NewOnly: true, Consumer: "reader"})
	incrementalOK(t, err)
	incrementalIDs(t, rows, "in-1")
}

func TestInboxDeliveryAcceptanceLargeBacklogStillStartsAtOldestUnseen(t *testing.T) {
	broker := inboxDeliveryBroker{backlog: 5051}
	n := inboxDeliveryNode(t, broker.answer)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := n.inbox(ctx, InboxArgs{From: "contact-a", NewOnly: true, Consumer: "reader", Limit: 1})
	incrementalOK(t, err)
	incrementalIDs(t, rows, "in-1")
	incrementalPageEnd(t, rows[0])
}

func TestInboxDeliveryAcceptanceWaitFindsIncomingInLargeBacklog(t *testing.T) {
	broker := inboxDeliveryBroker{backlog: 5051}
	n := inboxDeliveryNode(t, broker.answer)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	answer, err := n.waitInbox(ctx, WaitArgs{From: "contact-a", Consumer: "reader", TimeoutSeconds: 1})
	incrementalOK(t, err)
	if !answer.Ready {
		t.Fatal("wait missed already-present incoming messages")
	}
	rows, err := n.inbox(ctx, InboxArgs{From: "contact-a", After: 5050, Consumer: "reader"})
	incrementalOK(t, err)
	_, seen := incrementalPosition(t, rows[0])
	if seen != 0 {
		t.Fatalf("wait advanced seen to %d", seen)
	}
}
