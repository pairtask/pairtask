//go:build linux

package server

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func publicMessage(id string) protocol.Message {
	m := inboxMessage(id)
	m.To = "*"
	return m
}

func publicAccepted(t *testing.T, p *operatorPeer, m protocol.Message, recipients ...string) protocol.Message {
	t.Helper()
	p.write(m)
	accepted := p.read()
	requireOperatorAccepted(t, accepted, recipients...)
	operatorEqual(t, "no internal inbox identity", strings.Contains(sessionJSON(accepted), "operator-inbox"), false)
	return accepted
}

func TestPublicBroadcastAcceptanceOfflineOperatorAndHistory(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a, b := f.peer("alpha", 7), f.peer("beta", 7)
	accepted := publicAccepted(t, a, publicMessage("public"), "beta", "operator")
	peer := b.read()
	page := inboxReadPage(f, 0, 0, 32)
	operatorEqual(t, "operator gets one copy", len(page.Messages), 1)
	for _, m := range []protocol.Message{peer, page.Messages[0]} {
		operatorEqual(t, "shared message ID", m.MessageID, "public")
		operatorEqual(t, "shared sequence", m.Seq, accepted.Seq)
		operatorEqual(t, "public destination", m.To, "*")
		operatorEqual(t, "agent provenance", m.Origin, "agent")
		operatorEqual(t, "author retained", m.From, "alpha")
	}
	history := historyRead(f, 0, 0, 32)
	operatorEqual(t, "history includes public", len(history.Entries), 1)
	operatorEqual(t, "received direction", history.Entries[0].Direction, "received")
	operatorEqual(t, "history destination", history.Entries[0].Message.To, "*")
}

func TestPublicBroadcastAcceptanceSoloAndLiveEvents(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 7)
	p := sessionOpen(f)
	mark := p.snapshot().Mark
	publicAccepted(t, a, publicMessage("solo"), "operator")
	kinds := make(map[string]bool)
	for range 2 {
		e := p.event(&mark)
		kinds[e.Kind] = true
	}
	operatorEqual(t, "conversation event", kinds["conversation_changed"], true)
	operatorEqual(t, "inbox event", kinds["inbox_changed"], true)
	operatorEqual(t, "pending operator copy", p.snapshot().Pending, 1)
}

func TestPublicBroadcastAcceptanceLegacyAndPrivateAudience(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		to      string
	}{
		{"legacy broadcast", 6, "*"}, {"private direct", 7, "beta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			a, b := f.peer("alpha", tc.version), f.peer("beta", 7)
			m := publicMessage("private")
			m.To = tc.to
			publicAccepted(t, a, m, "beta")
			operatorEqual(t, "peer receives", b.read().MessageID, m.MessageID)
			operatorEqual(t, "operator excluded", len(inboxReadPage(f, 0, 0, 32).Messages), 0)
			operatorEqual(t, "operator history excludes private", len(historyRead(f, 0, 0, 32).Entries), 0)
		})
	}
}

func TestPublicBroadcastAcceptanceRequiresDurableAcknowledgments(t *testing.T) {
	for _, flags := range [][2]bool{{false, false}, {false, true}, {true, false}} {
		t.Run(fmt.Sprint(flags), func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			a := f.peer("alpha", 7)
			m := publicMessage("invalid")
			m.Durable, m.AckRequested = flags[0], flags[1]
			before := f.count("messages")
			a.write(m)
			operatorEqual(t, "invalid public flags", a.read().Code, "malformed_frame")
			operatorEqual(t, "no partial storage", f.count("messages"), before)
		})
	}
}

func TestPublicBroadcastAcceptanceOperatorQuotaIsAtomic(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 7)
	f.peer("beta", 7)
	for i := range 16 {
		inboxSend(t, a, inboxMessage(fmt.Sprintf("fill-%d", i)))
	}
	before := f.count("durable_inbox")
	messages := f.count("messages")
	a.write(publicMessage("overflow"))
	err := a.read()
	operatorEqual(t, "quota error", err.Code, "inbox_full")
	operatorEqual(t, "operator identified", strings.Contains(err.Detail, "operator"), true)
	operatorEqual(t, "no message stored", f.count("messages"), messages)
	operatorEqual(t, "no peer delivery persisted", f.count("durable_inbox"), before)
}

func TestPublicBroadcastAcceptanceStorageFailureIsAtomic(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 7)
	f.peer("beta", 7)
	inboxExec(f, `CREATE TRIGGER reject_public_operator BEFORE INSERT ON durable_inbox WHEN NEW.agent_id = 'operator' BEGIN SELECT RAISE(ABORT,'operator unavailable'); END`)
	before := f.count("messages")
	a.write(publicMessage("failed"))
	operatorEqual(t, "storage error", a.read().Code, "internal")
	operatorEqual(t, "no message stored", f.count("messages"), before)
	operatorEqual(t, "no inbox stored", f.count("durable_inbox"), 0)
}

func TestPublicBroadcastAcceptanceRetryFreezesMembership(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a, b := f.peer("alpha", 7), f.peer("beta", 7)
	m := publicMessage("retry")
	first := publicAccepted(t, a, m, "beta", "operator")
	b.read()
	f.peer("later", 7)
	again := publicAccepted(t, a, m, "beta", "operator")
	operatorEqual(t, "stable sequence", again.Seq, first.Seq)
	operatorEqual(t, "frozen recipient sessions", reflect.DeepEqual(again.Recipients, first.Recipients), true)
	operatorEqual(t, "one operator copy", len(inboxReadPage(f, 0, 0, 32).Messages), 1)
}

func TestPublicBroadcastAcceptanceRestartPreservesAudience(t *testing.T) {
	db := filepath.Join(t.TempDir(), "broker.db")
	var seq uint64
	t.Run("before", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), db)
		a, b := f.peer("alpha", 7), f.peer("beta", 7)
		seq = publicAccepted(t, a, publicMessage("restart"), "beta", "operator").Seq
		b.read()
	})
	t.Run("after", func(t *testing.T) {
		if seq == 0 {
			t.Skip("initial acceptance failed")
		}
		f := newOperatorFixture(t, os.Getuid(), db)
		b := f.peer("beta", 7)
		operatorEqual(t, "peer replay", b.read().Seq, seq)
		page := inboxReadPage(f, 0, 0, 32)
		operatorEqual(t, "retained operator copy", len(page.Messages), 1)
		operatorEqual(t, "same retained sequence", page.Messages[0].Seq, seq)
	})
}

func publicReplyParent(t *testing.T, f *operatorFixture, author, recipient *operatorPeer, kind string) {
	t.Helper()
	m := publicMessage("parent")
	switch kind {
	case "operator public":
		requireOperatorAccepted(t, f.send(operatorRequest("parent", "*")), "alpha", "beta")
		author.read()
		recipient.read()
	case "operator private":
		requireOperatorAccepted(t, f.send(operatorRequest("parent", "beta")), "beta")
		recipient.read()
	case "agent private":
		m.To = "beta"
		publicAccepted(t, author, m, "beta")
		recipient.read()
	case "unknown":
	default:
		publicAccepted(t, author, m, "beta", "operator")
		recipient.read()
	}
}

func TestPublicBroadcastAcceptanceReplyScope(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		allowed bool
	}{
		{"operator public", true}, {"agent public", true}, {"own public", true},
		{"operator private", false}, {"agent private", false}, {"unknown", false}, {"late outsider", false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			a, b := f.peer("alpha", 7), f.peer("beta", 7)
			publicReplyParent(t, f, a, b, tc.kind)
			sender := b
			if tc.kind == "own public" {
				sender = a
			}
			if tc.kind == "late outsider" {
				sender = f.peer("outsider", 7)
			}
			m := publicMessage("reply")
			m.InReplyTo = "parent"
			sender.write(m)
			result := sender.read()
			if tc.allowed {
				operatorEqual(t, "public reply accepted", result.Stage, "accepted")
				return
			}
			operatorEqual(t, "private reference rejected", result.Code, "invalid_reply")
		})
	}
}

func TestPublicBroadcastAcceptanceOperatorReceiptsStayExplicit(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("alpha", 7)
	publicAccepted(t, a, publicMessage("receipts"), "operator")
	inboxReadPage(f, 0, 0, 32)
	operatorEqual(t, "reading is not receipt", inboxReceiptCount(f, "received_at"), 0)
	operatorEqual(t, "receipt accepted", inboxReceived(f, "receipts").Stage, "adapter_received")
	inboxStage(t, a, "receipts", "adapter_received")
	operatorEqual(t, "receipt is not acknowledgment", inboxReceiptCount(f, "acknowledged_at"), 0)
	operatorEqual(t, "explicit acknowledgment", inboxAck(f, "receipts").Stage, "agent_acknowledged")
	inboxStage(t, a, "receipts", "agent_acknowledged")
}
