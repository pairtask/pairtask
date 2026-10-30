package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func routingRegister(t *testing.T, db *store.Store, agents ...string) {
	t.Helper()
	for _, agent := range agents {
		_, err := db.ClaimPendingFor(context.Background(), protocol.Recipient{AgentID: agent, SessionID: "session-" + agent}, 12)
		pairOK(t, err)
	}
}

func routingSameAgents(t *testing.T, got, want []string) {
	t.Helper()
	got, want = slices.Clone(got), slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("agents = %q, want %q", got, want)
	}
}

func TestRemoteRoutingAcceptanceGrantSnapshotAndAtomicLimit(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	s := db.PeerMessages()
	verbatim := "remote:agent/@\nname"
	routingRegister(t, db, "first", verbatim)
	shared, err := s.Share(ctx, contact, []string{"*"}, messageAt)
	pairOK(t, err)
	routingSameAgents(t, shared, []string{"first", verbatim})
	routingRegister(t, db, "later")
	shared, err = s.SharedAgents(ctx, contact)
	pairOK(t, err)
	routingSameAgents(t, shared, []string{"first", verbatim})
	// A future registration is not implicitly granted.
	for i := 0; i < 62; i++ {
		routingRegister(t, db, fmt.Sprintf("extra-%02d", i))
	}
	_, err = s.Share(ctx, contact, []string{"*"}, messageAt)
	if err == nil {
		t.Fatal("65-agent wildcard grant succeeded")
	}
	shared, err = s.SharedAgents(ctx, contact)
	pairOK(t, err)
	routingSameAgents(t, shared, []string{"first", verbatim})
	for _, invalid := range [][]string{{"operator"}, {""}, {"first", "*"}} {
		_, err = s.Share(ctx, contact, invalid, messageAt)
		if err == nil {
			t.Fatalf("invalid grant accepted: %q", invalid)
		}
	}
	shared, err = s.SharedAgents(ctx, contact)
	pairOK(t, err)
	routingSameAgents(t, shared, []string{"first", verbatim})
}

func TestRemoteRoutingAcceptanceAtomicGroupRefusalAndReplay(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	s := db.PeerMessages()
	routingRegister(t, db, "allowed", "withheld")
	_, err := s.Share(ctx, contact, []string{"allowed"}, messageAt)
	pairOK(t, err)
	m := messageWire("group", 1)
	m.From, m.To = "remote:sender/@\nname", []string{"allowed", "withheld"}
	a, err := s.Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, false)
	pairEqual(t, a.Refusal, messaging.Refuse(messaging.ReasonNotShared))
	pairEqual(t, len(messagePage(t, s, contact)), 0)
	pending, err := db.PendingImports(ctx, 100)
	pairOK(t, err)
	pairEqual(t, len(pending), 0)
	_, err = s.Share(ctx, contact, []string{"withheld"}, messageAt)
	pairOK(t, err)
	a, err = s.Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	a, err = s.Accept(ctx, contact, m, messageAt.Add(time.Minute))
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	pending, err = db.PendingImports(ctx, 100)
	pairOK(t, err)
	pairEqual(t, len(pending), 1)
	changed := m
	changed.To = []string{"allowed"}
	a, err = s.Accept(ctx, contact, changed, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Refusal, messaging.Refuse(messaging.ReasonConflict))
	marked, err := s.MarkRead(ctx, contact, []string{m.ID}, messageAt)
	pairOK(t, err)
	pairEqual(t, marked, 0)
	reads, err := s.PendingReads(ctx, contact, 100)
	pairOK(t, err)
	pairEqual(t, len(reads), 0)
}

func assertRoutingReplayConflicts(t *testing.T, s *store.PeerMessages, o messaging.Outgoing) {
	t.Helper()
	for _, field := range []string{"from", "to", "reply-id", "reply-author"} {
		changed := o
		switch field {
		case "from":
			changed.From = "other"
		case "to":
			changed.To = []string{"another-target"}
		case "reply-id":
			changed.ReplyTo.ID = "other-parent"
		case "reply-author":
			changed.ReplyTo.Author = messaging.ReplyBySender
		}
		_, err := s.Queue(context.Background(), changed)
		if !errors.Is(err, messaging.ErrConflict) {
			t.Fatalf("changed %s: %v", field, err)
		}
	}
}

func routingLimit(version byte) messaging.Limit {
	limit := messaging.SessionLimit
	limit.Version = version
	return limit
}

func TestRemoteRoutingAcceptanceQueueBindsRoutingAndDowngrade(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	s := db.PeerMessages()
	routingRegister(t, db, "sender", "other")
	_, err := s.Share(ctx, contact, []string{"sender", "other"}, messageAt)
	pairOK(t, err)
	o := messaging.Outgoing{Contact: contact, ID: "out", Text: "payload", Now: messageAt, From: "sender", To: []string{"remote-target"}, ReplyTo: messaging.ReplyRef{ID: "parent", Author: messaging.ReplyByReceiver}}
	first, err := s.Queue(ctx, o)
	pairOK(t, err)
	again, err := s.Queue(ctx, o)
	pairOK(t, err)
	pairEqual(t, again.Cursor, first.Cursor)
	assertRoutingReplayConflicts(t, s, o)
	for _, version := range []byte{1, 2} {
		rows, err := s.Offer(ctx, contact, messageAt, routingLimit(version))
		pairOK(t, err)
		pairEqual(t, len(rows), 0)
		held := messagePage(t, s, contact)[0]
		pairEqual(t, held.State, messaging.StateQueued)
		pairEqual(t, held.Refusal, messaging.Refusal{Class: messaging.Transient, Reason: messaging.ReasonPeerUpgrade})
	}
	rows, err := s.Offer(ctx, contact, messageAt, routingLimit(3))
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
	if !reflect.DeepEqual(rows[0].To, o.To) {
		t.Fatalf("recipients changed: %q", rows[0].To)
	}
	_, err = s.Unshare(ctx, contact, []string{"sender"}, messageAt)
	pairOK(t, err)
	rows, err = s.Offer(ctx, contact, messageAt, messaging.SessionLimit)
	pairOK(t, err)
	pairEqual(t, len(rows), 0)
	row := messagePage(t, s, contact)[0]
	pairEqual(t, row.State, messaging.StateRefused)
	pairEqual(t, row.Refusal, messaging.Refuse(messaging.ReasonNotShared))
}

func TestRemoteRoutingAcceptanceUpgradeHoldClearsOnDelivery(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	s := db.PeerMessages()
	_, err := s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: "upgrade", Text: "payload", Now: messageAt, To: []string{"remote-target"}})
	pairOK(t, err)
	_, err = s.Offer(ctx, contact, messageAt, routingLimit(2))
	pairOK(t, err)
	pairEqual(t, messagePage(t, s, contact)[0].Refusal.Reason, messaging.ReasonPeerUpgrade)
	rows, err := s.Offer(ctx, contact, messageAt, routingLimit(3))
	pairOK(t, err)
	pairEqual(t, len(rows), 1)
	pairOK(t, s.Delivered(ctx, contact, "upgrade", messageAt))
	delivered := messagePage(t, s, contact)[0]
	pairEqual(t, delivered.State, messaging.StateDelivered)
	pairEqual(t, delivered.Refusal, messaging.Refusal{})
}

func TestRemoteRoutingAcceptancePendingImportSurvivesRestartAndCollection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.db")
	db := pairSQLite(t, path)
	contact := messageContact(t, db, 1)
	ctx := context.Background()
	routingRegister(t, db, "recipient")
	_, err := db.PeerMessages().Share(ctx, contact, []string{"recipient"}, messageAt)
	pairOK(t, err)
	m := messageWire("survive", 1)
	m.To = []string{"recipient"}
	a, err := db.PeerMessages().Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	pairOK(t, db.Close())
	db = pairSQLite(t, path)
	pairOK(t, db.PeerMessages().Collect(ctx, messageAt.Add(365*24*time.Hour)))
	pending, err := db.PendingImports(ctx, 10)
	pairOK(t, err)
	pairEqual(t, len(pending), 1)
	_, err = db.ImportRemote(ctx, pending[0], 1, messageAt.Add(time.Hour))
	pairOK(t, err)
	// Retry the same staged object after commit: never allocate another inbox row.
	_, err = db.ImportRemote(ctx, pending[0], 2, messageAt.Add(2*time.Hour))
	pairOK(t, err)
	old, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "recipient", SessionID: "old"}, 11)
	pairOK(t, err)
	pairEqual(t, len(old), 0)
	got, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "recipient", SessionID: "new"}, 12)
	pairOK(t, err)
	pairEqual(t, len(got), 1)
	pairEqual(t, got[0].Origin, "remote")
	pairEqual(t, got[0].Seq, uint64(1))
	var payload struct {
		Text string `json:"text"`
	}
	pairOK(t, json.Unmarshal(got[0].Payload, &payload))
	pairEqual(t, payload.Text, m.Text)
	if got[0].Remote == nil {
		t.Fatal("remote provenance lost on replay")
	}
	pairEqual(t, got[0].Remote.Contact, contact)
	remaining, err := db.PendingImports(ctx, 10)
	pairOK(t, err)
	pairEqual(t, len(remaining), 0)
}

type routingScope struct {
	db      *store.Store
	contact string
}

func (scope routingScope) stage(t *testing.T, id string, recipients ...string) store.RemoteImport {
	t.Helper()
	db, contact := scope.db, scope.contact
	m := messageWire(id, 1)
	m.From, m.To = "remote-sender", recipients
	a, err := db.PeerMessages().Accept(context.Background(), contact, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	imports, err := db.PendingImports(context.Background(), 1000)
	pairOK(t, err)
	for _, imp := range imports {
		if imp.Contact == contact && imp.RemoteID == id {
			return imp
		}
	}
	t.Fatal("accepted routed message has no staged import")
	return store.RemoteImport{}
}

type routingLocalSender struct {
	db        *store.Store
	recipient string
}

func (sender routingLocalSender) persist(t *testing.T, id string, seq uint64) {
	t.Helper()
	db, recipient := sender.db, sender.recipient
	at := messageAt
	m := protocol.Message{Type: protocol.TypeMsg, MessageID: id, Seq: seq, TS: &at, From: "local-sender", To: recipient, Payload: []byte(`{"text":"local content"}`), Durable: true, AckRequested: true}
	pairOK(t, db.PersistMessage(context.Background(), m, "local-session", []protocol.Recipient{{AgentID: recipient, SessionID: "session-" + recipient}}))
}

func TestRemoteRoutingAcceptanceCollisionBetweenStageAndImport(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	routingRegister(t, db, "recipient", "local-recipient")
	_, err := db.PeerMessages().Share(ctx, contact, []string{"recipient"}, messageAt)
	pairOK(t, err)
	imp := (routingScope{db, contact}).stage(t, "same-id", "recipient")
	// A local sender takes the candidate AFTER Accept reserved the import.
	(routingLocalSender{db, "local-recipient"}).persist(t, imp.LocalID, 1)
	stale := imp
	stale.Sender, stale.Text, stale.Recipients = "operator", "forged stale content", []string{"local-recipient"}
	result, err := db.ImportRemote(ctx, stale, 2, messageAt)
	pairOK(t, err)
	pairEqual(t, result.State, store.ImportImported)
	if result.Msg.MessageID == imp.LocalID {
		t.Fatal("import reused colliding local message")
	}
	pairEqual(t, result.Msg.From, "remote-sender")
	pairEqual(t, result.Msg.Origin, "remote")
	got, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "recipient", SessionID: "new"}, 12)
	pairOK(t, err)
	pairEqual(t, len(got), 1)
	pairEqual(t, got[0].MessageID, result.Msg.MessageID)
	local, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "local-recipient", SessionID: "new-local"}, 12)
	pairOK(t, err)
	pairEqual(t, len(local), 1)
	pairEqual(t, local[0].MessageID, imp.LocalID)
	pairEqual(t, local[0].From, "local-sender")
	if local[0].Remote != nil {
		t.Fatal("collision contaminated local provenance")
	}
	replay, err := db.ImportRemote(ctx, imp, 3, messageAt)
	pairOK(t, err)
	pairEqual(t, replay.Changed, false)
}

func TestRemoteRoutingAcceptanceRevocationWithholdsEntireFrozenGroup(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	routingRegister(t, db, "first", "second")
	_, err := db.PeerMessages().Share(ctx, contact, []string{"first", "second"}, messageAt)
	pairOK(t, err)
	imp := (routingScope{db, contact}).stage(t, "revoke", "first", "second")
	_, err = db.PeerMessages().Unshare(ctx, contact, []string{"second"}, messageAt)
	pairOK(t, err)
	_, err = db.PeerMessages().Share(ctx, contact, []string{"second"}, messageAt)
	pairOK(t, err)
	result, err := db.ImportRemote(ctx, imp, 1, messageAt)
	pairOK(t, err)
	pairEqual(t, result.State, store.ImportWithheld)
	for _, agent := range []string{"first", "second"} {
		got, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: agent, SessionID: "new-" + agent}, 12)
		pairOK(t, err)
		pairEqual(t, len(got), 0)
	}
	reads, err := db.PeerMessages().PendingAgentReads(ctx, contact, 100)
	pairOK(t, err)
	pairEqual(t, len(reads), 0)
}

func TestRemoteRoutingAcceptanceFullInboxRollsBackWholeGroupAndRetries(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	routingRegister(t, db, "available", "full")
	_, err := db.PeerMessages().Share(ctx, contact, []string{"available", "full"}, messageAt)
	pairOK(t, err)
	for i := 0; i < store.MaxPendingMessages; i++ {
		(routingLocalSender{db, "full"}).persist(t, fmt.Sprintf("fill-%d", i), uint64(i+1))
	}
	imp := (routingScope{db, contact}).stage(t, "wait-capacity", "available", "full")
	_, err = db.ImportRemote(ctx, imp, 100, messageAt)
	if !errors.Is(err, store.ErrInboxFull) {
		t.Fatalf("full recipient: %v", err)
	}
	got, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "available", SessionID: "new"}, 12)
	pairOK(t, err)
	pairEqual(t, len(got), 0)
	pending, err := db.PendingImports(ctx, 100)
	pairOK(t, err)
	pairEqual(t, len(pending), 1)
	for _, stage := range []string{protocol.StageAdapterReceived, protocol.StageAgentAcknowledged} {
		_, err = db.Acknowledge(ctx, store.Acknowledgment{MessageID: "fill-0", Recipient: protocol.Recipient{AgentID: "full", SessionID: "session-full"}, Stage: stage, ProtocolVersion: 12})
		pairOK(t, err)
	}
	result, err := db.ImportRemote(ctx, imp, 100, messageAt)
	pairOK(t, err)
	pairEqual(t, result.State, store.ImportImported)
	pairEqual(t, len(result.Recipients), 2)
	got, err = db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "available", SessionID: "new"}, 12)
	pairOK(t, err)
	pairEqual(t, len(got), 1)
}

func TestRemoteRoutingAcceptanceUnshareWithholdsImportedReplay(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	routingRegister(t, db, "recipient")
	_, err := db.PeerMessages().Share(ctx, contact, []string{"recipient"}, messageAt)
	pairOK(t, err)
	imp := (routingScope{db, contact}).stage(t, "already-imported", "recipient")
	_, err = db.ImportRemote(ctx, imp, 1, messageAt)
	pairOK(t, err)
	_, err = db.PeerMessages().Unshare(ctx, contact, []string{"recipient"}, messageAt)
	pairOK(t, err)
	got, err := db.ClaimPendingFor(ctx, protocol.Recipient{AgentID: "recipient", SessionID: "reconnect"}, 12)
	pairOK(t, err)
	pairEqual(t, len(got), 0)
	reads, err := db.PeerMessages().PendingAgentReads(ctx, contact, 100)
	pairOK(t, err)
	pairEqual(t, len(reads), 0)
}

func TestRemoteRoutingAcceptanceAgentReceiptsAreRecipientSpecific(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	routingRegister(t, db, "first", "second", "outsider")
	s := db.PeerMessages()
	_, err := s.Share(ctx, contact, []string{"first", "second"}, messageAt)
	pairOK(t, err)
	imp := (routingScope{db, contact}).stage(t, "agent-receipts", "first", "second")
	result, err := db.ImportRemote(ctx, imp, 1, messageAt)
	pairOK(t, err)
	for i := 0; i < 2; i++ {
		pairOK(t, s.QueueAgentRead(ctx, result.Msg.MessageID, "first", messageAt))
	}
	if err := s.QueueAgentRead(ctx, result.Msg.MessageID, "outsider", messageAt); err == nil {
		t.Fatal("non-recipient agent receipt accepted")
	}
	reads, err := s.PendingAgentReads(ctx, contact, 100)
	pairOK(t, err)
	if !reflect.DeepEqual(reads, []messaging.AgentRead{{ID: imp.RemoteID, Agent: "first"}}) {
		t.Fatalf("receipts = %#v", reads)
	}
	// A confirmation for another member cannot consume first's pending ack.
	pairOK(t, s.AgentReadConfirmed(ctx, contact, messaging.AgentRead{ID: imp.RemoteID, Agent: "second"}))
	reads, err = s.PendingAgentReads(ctx, contact, 100)
	pairOK(t, err)
	pairEqual(t, len(reads), 1)
	pairOK(t, s.AgentReadConfirmed(ctx, contact, reads[0]))
	reads, err = s.PendingAgentReads(ctx, contact, 100)
	pairOK(t, err)
	pairEqual(t, len(reads), 0)
}

func TestRemoteRoutingAcceptanceChosenOutboundIDCannotForgeLocalParent(t *testing.T) {
	db, contact := messageFixture(t)
	ctx := context.Background()
	routingRegister(t, db, "recipient")
	s := db.PeerMessages()
	_, err := s.Share(ctx, contact, []string{"recipient"}, messageAt)
	pairOK(t, err)
	(routingLocalSender{db, "recipient"}).persist(t, "chosen-parent", 1)
	// The operator's contact message ID happens to equal an unrelated local ID.
	_, err = s.Queue(ctx, messaging.Outgoing{Contact: contact, ID: "chosen-parent", Text: "operator contact text", Now: messageAt})
	pairOK(t, err)
	m := messageWire("reply", 1)
	m.From, m.To = "remote-sender", []string{"recipient"}
	m.ReplyTo = messaging.ReplyRef{ID: "chosen-parent", Author: messaging.ReplyByReceiver}
	a, err := s.Accept(ctx, contact, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	imports, err := db.PendingImports(ctx, 10)
	pairOK(t, err)
	pairEqual(t, len(imports), 1)
	result, err := db.ImportRemote(ctx, imports[0], 2, messageAt)
	pairOK(t, err)
	pairEqual(t, result.Msg.InReplyTo, "")
	if result.Msg.Remote == nil {
		t.Fatal("claimed reply metadata lost")
	}
	pairEqual(t, result.Msg.Remote.ReplyID, "chosen-parent")
	pairEqual(t, result.Msg.Remote.ReplyAuthor, messaging.ReplyByReceiver)
}

func TestRemoteRoutingAcceptanceDirectoryRejectsInvalidReplacementAtomically(t *testing.T) {
	oversized := make([]string, 65)
	for i := range oversized {
		oversized[i] = fmt.Sprintf("agent-%d", i)
	}
	for _, invalid := range [][]string{{"operator"}, {"*"}, {""}, {strings.Repeat("x", 129)}, oversized} {
		t.Run(fmt.Sprintf("%q", invalid), func(t *testing.T) {
			db, contact := messageFixture(t)
			ctx := context.Background()
			s := db.PeerMessages()
			original := []string{"remote:verbatim/@\nname"}
			pairOK(t, s.SetPeerDirectory(ctx, contact, original, messageAt))
			if err := s.SetPeerDirectory(ctx, contact, invalid, messageAt); err == nil {
				t.Fatalf("invalid replacement accepted: %q", invalid)
			}
			got, err := s.PeerDirectory(ctx, contact)
			pairOK(t, err)
			routingSameAgents(t, got, original)
		})
	}
}
