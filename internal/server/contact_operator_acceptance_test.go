//go:build linux

package server

import (
	"context"
	"fmt"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestContactOperatorAcceptanceWireReadAndIsolation(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	key := id.Fingerprint()
	ctx := context.Background()
	answer, err := f.st.PeerMessages().Accept(ctx, key, messaging.Message{ID: "incoming", N: 1, Text: "PRIVATE CONTACT TEXT", CreatedAt: time.Now()}, time.Now())
	operatorNoError(t, err)
	operatorEqual(t, "stored", answer.Stored, true)
	auditBefore := f.count("operator_audit")
	list := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: key}
	r := f.send(list)
	operatorEqual(t, "list type", r.Type, protocol.TypeOperatorContactResult)
	if r.ContactPage == nil || len(r.ContactPage.Messages) != 1 {
		t.Fatalf("page: %+v", r)
	}
	operatorEqual(t, "list leaves unread", r.ContactPage.Messages[0].State, messaging.StateReceived)
	read := protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpRead, ContactKey: key, MessageID: "read-operation", ContactRead: []string{"incoming"}}
	r = f.send(read)
	if r.ContactMarked == nil || *r.ContactMarked != 1 {
		t.Fatalf("read: %+v", r)
	}
	r = f.send(read)
	if r.ContactMarked == nil || *r.ContactMarked != 0 {
		t.Fatalf("read replay: %+v", r)
	}
	receipts, err := f.st.PeerMessages().PendingReads(ctx, key, 64)
	operatorNoError(t, err)
	operatorEqual(t, "one receipt", len(receipts), 1)
	for _, table := range []string{"messages", "durable_inbox"} {
		operatorEqual(t, table, f.count(table), 0)
	}
	agent := f.peer("contact-agent", protocol.Version)
	agent.write(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend, ContactKey: key, MessageID: "forged", ContactText: "forged"})
	operatorEqual(t, "agent forbidden", agent.read().Code, protocol.ErrForbidden)
	operatorEqual(t, "no forged audit", f.count("operator_audit"), auditBefore+2)
}

func TestContactOperatorAcceptanceAuditFailureNoQueue(t *testing.T) {
	for _, event := range []string{"INSERT", "UPDATE"} {
		t.Run(event, func(t *testing.T) {
			f := newRedeemOperator(t, redeemConfig(), "")
			id := messageIdentity(t)
			messageConfirm(f, id, 1)
			_, err := f.db.Exec("CREATE TRIGGER deny_contact_audit BEFORE " + event + " ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END")
			operatorNoError(t, err)
			r := f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend, ContactKey: id.Fingerprint(), MessageID: "audit-failed", ContactText: "private"})
			operatorEqual(t, "failure", r.Code, protocol.ErrInternal)
			operatorEqual(t, "no queue", f.count("peer_messages"), 0)
			operatorEqual(t, "no operation", f.count("operator_contact_requests"), 0)
		})
	}
}

type contactPageProbe struct {
	fixture   *redeemOperatorFixture
	key, text string
	seen      map[string]bool
	before    int64
}

func newContactPageProbe(t *testing.T) *contactPageProbe {
	t.Helper()
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	p := &contactPageProbe{fixture: f, key: id.Fingerprint(), text: strings.Repeat("\x00", protocol.ContactTextBytes), seen: map[string]bool{}}
	for n := 1; n <= 12; n++ {
		answer, err := f.st.PeerMessages().Accept(context.Background(), p.key, messaging.Message{ID: fmt.Sprint("large-", n), N: uint64(n), CreatedAt: time.Now(), Text: p.text}, time.Now())
		operatorNoError(t, err)
		operatorEqual(t, "seed accepted", answer.Stored, true)
	}
	return p
}
func (p *contactPageProbe) next(t *testing.T) bool {
	t.Helper()
	r := p.fixture.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: p.key, ContactBefore: p.before})
	if r.ContactPage == nil {
		t.Fatalf("missing page: %+v", r)
	}
	operatorEqual(t, "nonempty page", len(r.ContactPage.Messages) > 0, true)
	for _, m := range r.ContactPage.Messages {
		operatorEqual(t, "no duplicate "+m.ID, p.seen[m.ID], false)
		p.seen[m.ID] = true
		operatorEqual(t, "text preserved", m.Text, p.text)
		p.before = m.Cursor
	}
	return r.ContactPage.More
}
func TestContactOperatorAcceptancePageBoundsEncodedBytes(t *testing.T) {
	p := newContactPageProbe(t)
	for pages := 0; pages < 12; pages++ {
		if !p.next(t) {
			break
		}
	}
	operatorEqual(t, "every row reachable", len(p.seen), 12)
}
