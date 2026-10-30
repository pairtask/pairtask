package hub

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func remoteHubFixture(t *testing.T) (*Hub, string) {
	t.Helper()
	h := newTestHub(t)
	return h, remoteHubContact(t, h, 1)
}

func remoteHubContact(t *testing.T, h *Hub, n int) string {
	t.Helper()
	ctx := context.Background()
	key := fmt.Sprintf("sha256:%064x", n)
	at := time.Now()
	_, err := h.store.Commit(ctx, peer.PairingOutcome{OperationID: fmt.Sprintf("remote-op-%d", n), InvitationID: fmt.Sprintf("remote-inv-%d", n), IdentityKey: key, Name: "Remote workspace", TranscriptID: make([]byte, 64), At: at})
	messageWorkerOK(t, err)
	_, err = h.store.Accept(ctx, key)
	messageWorkerOK(t, err)
	_, err = h.store.Deliver(ctx, key, peer.PairingAccepted, at)
	messageWorkerOK(t, err)
	return key
}

func remoteHubClient(t *testing.T, h *Hub, id string, version int) *Client {
	t.Helper()
	c := newClient(id)
	c.ProtocolVersion = version
	_, err := h.store.ClaimPendingFor(context.Background(), c.identity(), version)
	messageWorkerOK(t, err)
	return c
}

func remoteHubShare(t *testing.T, h *Hub, key string, agents ...string) {
	t.Helper()
	_, err := h.store.PeerMessages().Share(context.Background(), key, agents, time.Now())
	messageWorkerOK(t, err)
}

func remoteHubStage(t *testing.T, h *Hub, key string, m messaging.Message) {
	t.Helper()
	m.CreatedAt = time.Now()
	m.Text = "from the paired workspace"
	a, err := h.store.PeerMessages().Accept(context.Background(), key, m, time.Now())
	messageWorkerOK(t, err)
	messageSyncEqual(t, a.Stored, true)
}

func remoteHubEmpty(t *testing.T, c *Client) {
	t.Helper()
	select {
	case m := <-c.Send:
		t.Fatalf("unexpected delivery to %s: %+v", c.ID, m)
	default:
	}
}

func TestRemoteHubAcceptanceVersionGateAndOperatorProvenance(t *testing.T) {
	h, key := remoteHubFixture(t)
	old := remoteHubClient(t, h, "old", 11)
	current := remoteHubClient(t, h, "current", 12)
	remoteHubShare(t, h, key, old.ID, current.ID)
	remoteHubStage(t, h, key, messaging.Message{ID: "operator-message", N: 1, To: []string{old.ID, current.ID}})
	h.drainImports(context.Background(), map[string]*Client{old.ID: old, current.ID: current})
	remoteHubEmpty(t, old)
	got := recv(t, current)
	messageSyncEqual(t, got.Origin, protocol.OriginRemote)
	messageSyncEqual(t, got.From, protocol.OperatorID)
	if got.Remote == nil {
		t.Fatal("remote operator provenance missing")
	}
	messageSyncEqual(t, got.Remote.Contact, key)
	messageSyncEqual(t, got.Remote.Sender, "")
	messageSyncEqual(t, len(got.Group), 2)
	old.SessionID = "upgraded"
	replay, err := h.store.ClaimPendingFor(context.Background(), old.identity(), 12)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(replay), 1)
	messageSyncEqual(t, replay[0].MessageID, got.MessageID)
	messageSyncEqual(t, replay[0].Origin, protocol.OriginRemote)
}

func remoteHubImported(t *testing.T, h *Hub, key string, recipient *Client) protocol.Message {
	t.Helper()
	remoteHubShare(t, h, key, recipient.ID)
	remoteHubStage(t, h, key, messaging.Message{ID: "parent", N: 1, From: "remote-agent", To: []string{recipient.ID}})
	h.drainImports(context.Background(), map[string]*Client{recipient.ID: recipient})
	return recv(t, recipient)
}

func remoteHubReply(parent protocol.Message, id string) protocol.Message {
	return protocol.Message{Type: protocol.TypeMsg, MessageID: id, To: parent.From, InReplyTo: parent.MessageID, Payload: []byte(`{"text":"reply"}`), AckRequested: true, Durable: true}
}

func TestRemoteHubAcceptanceReplyRequiresFrozenParticipantAndCurrentGrant(t *testing.T) {
	h, key := remoteHubFixture(t)
	a := remoteHubClient(t, h, "participant", 12)
	b := remoteHubClient(t, h, "outsider", 12)
	parent := remoteHubImported(t, h, key, a)
	remoteHubShare(t, h, key, b.ID)
	clients := map[string]*Client{a.ID: a, b.ID: b}
	h.route(context.Background(), clients, Inbound{From: b, Msg: remoteHubReply(parent, "not-participant")})
	messageSyncEqual(t, recv(t, b).Type, protocol.TypeError)
	_, err := h.store.PeerMessages().Unshare(context.Background(), key, []string{a.ID}, time.Now())
	messageWorkerOK(t, err)
	h.route(context.Background(), clients, Inbound{From: a, Msg: remoteHubReply(parent, "revoked")})
	messageSyncEqual(t, recv(t, a).Code, protocol.ErrRecipientUnavailable)
	rows, err := h.store.PeerMessages().Offer(context.Background(), key, time.Now(), messaging.SessionLimit)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(rows), 0)
}

func TestRemoteHubAcceptanceReplyRetryReturnsAcceptedWithoutSecondQueue(t *testing.T) {
	h, key := remoteHubFixture(t)
	a := remoteHubClient(t, h, "participant", 12)
	parent := remoteHubImported(t, h, key, a)
	clients := map[string]*Client{a.ID: a}
	in := Inbound{From: a, Msg: remoteHubReply(parent, "stable-reply")}
	h.route(context.Background(), clients, in)
	first := recv(t, a)
	messageSyncEqual(t, first.Stage, protocol.StageAccepted)
	h.route(context.Background(), clients, in)
	retry := recv(t, a)
	messageSyncEqual(t, retry.Type, protocol.TypeAck)
	messageSyncEqual(t, retry.Stage, protocol.StageAccepted)
	messageSyncEqual(t, retry.Seq, first.Seq)
	rows, err := h.store.PeerMessages().Offer(context.Background(), key, time.Now(), messaging.SessionLimit)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(rows), 1)
	messageSyncEqual(t, rows[0].From, a.ID)
	messageSyncEqual(t, rows[0].ReplyTo.ID, "parent")
	messageSyncEqual(t, rows[0].ReplyTo.Author, messaging.ReplyByReceiver)
}

func TestRemoteHubAcceptanceAgentCannotForgeRemoteProvenance(t *testing.T) {
	h := newTestHub(t)
	a := remoteHubClient(t, h, "sender", 12)
	b := remoteHubClient(t, h, "recipient", 12)
	m := protocol.Message{Type: protocol.TypeMsg, MessageID: "forged", To: b.ID, Payload: []byte(`{"text":"local message"}`), Remote: &protocol.Remote{Contact: "forged-contact", Sender: "operator", Label: "Trusted workspace"}}
	h.route(context.Background(), map[string]*Client{a.ID: a, b.ID: b}, Inbound{From: a, Msg: m})
	got := recv(t, b)
	messageSyncEqual(t, got.Origin, protocol.OriginAgent)
	messageSyncEqual(t, got.From, a.ID)
	if got.Remote != nil {
		t.Fatalf("agent-supplied remote provenance escaped: %+v", got.Remote)
	}
}

func TestRemoteHubAcceptanceAdapterReceiptIsNotAgentRead(t *testing.T) {
	h, key := remoteHubFixture(t)
	a := remoteHubClient(t, h, "participant", 12)
	parent := remoteHubImported(t, h, key, a)
	clients := map[string]*Client{a.ID: a}
	ack := protocol.Message{Type: protocol.TypeAck, MessageID: parent.MessageID, Stage: protocol.StageAdapterReceived}
	h.acknowledge(context.Background(), clients, Inbound{From: a, Msg: ack})
	reads, err := h.store.PeerMessages().PendingAgentReads(context.Background(), key, 10)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(reads), 0)
	ack.Stage = protocol.StageAgentAcknowledged
	h.acknowledge(context.Background(), clients, Inbound{From: a, Msg: ack})
	messageSyncEqual(t, recv(t, a).Stage, protocol.StageAgentAcknowledged)
	reads, err = h.store.PeerMessages().PendingAgentReads(context.Background(), key, 10)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(reads), 1)
	messageSyncEqual(t, reads[0], messaging.AgentRead{ID: "parent", Agent: a.ID})
}

func TestRemoteHubAcceptanceFullRecipientCannotStarveLaterHealthyImport(t *testing.T) {
	h, key := remoteHubFixture(t)
	full := remoteHubClient(t, h, "full", 12)
	healthy := remoteHubClient(t, h, "healthy", 12)
	remoteHubShare(t, h, key, full.ID, healthy.ID)
	ctx := context.Background()
	for i := 0; i < store.MaxPendingMessages; i++ {
		at := time.Now()
		m := protocol.Message{Type: protocol.TypeMsg, MessageID: fmt.Sprintf("fill-%d", i), Seq: uint64(i + 1), TS: &at, From: "local", To: full.ID, Payload: []byte(`{"text":"fill"}`), Durable: true, AckRequested: true}
		messageWorkerOK(t, h.store.PersistMessage(ctx, m, "local", []protocol.Recipient{full.identity()}))
	}
	h.seq = store.MaxPendingMessages
	for i := 0; i < 65; i++ {
		remoteHubStage(t, h, key, messaging.Message{ID: fmt.Sprintf("blocked-%d", i), N: uint64(i + 1), To: []string{full.ID}})
	}
	remoteHubStage(t, h, key, messaging.Message{ID: "healthy-after-blocked", N: 66, To: []string{healthy.ID}})
	clients := map[string]*Client{full.ID: full, healthy.ID: healthy}
	// Repeated wake signals must eventually reach later work, without requiring
	// the unrelated full recipient to acknowledge anything first.
	for range 4 {
		h.drainImports(ctx, clients)
	}
	select {
	case m := <-healthy.Send:
		messageSyncEqual(t, m.Origin, protocol.OriginRemote)
	default:
		t.Fatal("full first batch starved healthy import behind it")
	}
}

func TestRemoteHubAcceptanceStartupAndRoundCompletionWakeImport(t *testing.T) {
	for _, startup := range []bool{true, false} {
		t.Run(fmt.Sprintf("startup=%v", startup), func(t *testing.T) {
			h, key := remoteHubFixture(t)
			a := remoteHubClient(t, h, "recipient", 12)
			remoteHubShare(t, h, key, a.ID)
			stage := func() { remoteHubStage(t, h, key, messaging.Message{ID: "wake-import", N: 1, To: []string{a.ID}}) }
			if startup {
				stage()
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(func() { cancel(); <-h.Done() })
			go h.Run(ctx)
			if !h.Register(ctx, a) {
				t.Fatal("registration failed")
			}
			messageSyncEqual(t, recv(t, a).Type, protocol.TypeWelcome)
			if !startup {
				stage()
				end, ok := h.beginMessages(key)
				messageSyncEqual(t, ok, true)
				end(true)
			}
			got := recv(t, a)
			messageSyncEqual(t, got.Type, protocol.TypeMsg)
			messageSyncEqual(t, got.Origin, protocol.OriginRemote)
			if got.Remote == nil {
				t.Fatal("import wake lost provenance")
			}
			messageSyncEqual(t, got.Remote.Contact, key)
		})
	}
}

func TestRemoteHubAcceptanceCommittedAckRecoversPeerReceiptAfterRestart(t *testing.T) {
	h, key := remoteHubFixture(t)
	a := remoteHubClient(t, h, "recipient", 12)
	parent := remoteHubImported(t, h, key, a)
	ctx := context.Background()
	for _, stage := range []string{protocol.StageAdapterReceived, protocol.StageAgentAcknowledged} {
		_, err := h.store.AcknowledgeReport(ctx, store.Acknowledgment{MessageID: parent.MessageID, Recipient: a.identity(), Stage: stage, ProtocolVersion: 12})
		messageWorkerOK(t, err)
	}
	// Simulate process loss after the durable acknowledgment commit, before
	// the hub's post-commit callback. Startup must recover the peer receipt.
	restarted := New(h.store, h.seq, h.log)
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(func() { cancel(); <-restarted.Done() })
	go restarted.Run(runCtx)
	observer := newClient("startup-barrier")
	if !restarted.Register(runCtx, observer) {
		t.Fatal("restart registration failed")
	}
	messageSyncEqual(t, recv(t, observer).Type, protocol.TypeWelcome)
	reads, err := h.store.PeerMessages().PendingAgentReads(ctx, key, 10)
	messageWorkerOK(t, err)
	messageSyncEqual(t, len(reads), 1)
	messageSyncEqual(t, reads[0], messaging.AgentRead{ID: "parent", Agent: a.ID})
}

func remoteHubScanFixture(t *testing.T) (*Hub, *Client, *Client) {
	t.Helper()
	h, first := remoteHubFixture(t)
	full := remoteHubClient(t, h, "full", 12)
	healthy := remoteHubClient(t, h, "healthy", 12)
	ctx := context.Background()
	for i := 0; i < store.MaxPendingMessages; i++ {
		at := time.Now()
		m := protocol.Message{Type: protocol.TypeMsg, MessageID: fmt.Sprintf("fill-%d", i), Seq: uint64(i + 1), TS: &at, From: "local", To: full.ID, Payload: []byte(`{"text":"fill"}`), Durable: true, AckRequested: true}
		messageWorkerOK(t, h.store.PersistMessage(ctx, m, "local", []protocol.Recipient{full.identity()}))
	}
	h.seq = store.MaxPendingMessages
	for n := 1; n <= 4; n++ {
		key := first
		if n > 1 {
			key = remoteHubContact(t, h, n)
		}
		remoteHubShare(t, h, key, full.ID)
		for i := 0; i < 256; i++ {
			remoteHubStage(t, h, key, messaging.Message{ID: fmt.Sprintf("blocked-%d", i), N: uint64(i + 1), To: []string{full.ID}})
		}
	}
	last := remoteHubContact(t, h, 5)
	remoteHubShare(t, h, last, healthy.ID)
	remoteHubStage(t, h, last, messaging.Message{ID: "healthy-after-scan-limit", N: 1, To: []string{healthy.ID}})
	return h, full, healthy
}

func TestRemoteHubAcceptanceScanLimitCannotStarveLaterContact(t *testing.T) {
	h, full, healthy := remoteHubScanFixture(t)
	ctx := context.Background()
	clients := map[string]*Client{full.ID: full, healthy.ID: healthy}
	for range 4 {
		h.drainImports(ctx, clients)
	}
	select {
	case m := <-healthy.Send:
		messageSyncEqual(t, m.Origin, protocol.OriginRemote)
	default:
		t.Fatal("bounded scan restarted at blocked prefix and starved later contact")
	}
}

func TestRemoteHubAcceptanceAckDuringScanRevisitsEarlierBlockedRows(t *testing.T) {
	h, full, healthy := remoteHubScanFixture(t)
	ctx := context.Background()
	clients := map[string]*Client{full.ID: full, healthy.ID: healthy}
	h.drainImports(ctx, clients) // blocked prefix scanned; continuation is queued
	for _, stage := range []string{protocol.StageAdapterReceived, protocol.StageAgentAcknowledged} {
		h.acknowledge(ctx, clients, Inbound{From: full, Msg: protocol.Message{Type: protocol.TypeAck, MessageID: "fill-0", Stage: stage}})
	}
	messageSyncEqual(t, recv(t, full).Stage, protocol.StageAgentAcknowledged)
	// Process only actual pending wake signals, as Run would. The ack's wake
	// shares the one-slot channel with the continuation already queued.
	for range 4 {
		select {
		case <-h.pair.changed:
			h.background(ctx, clients)
		default:
		}
	}
	select {
	case m := <-full.Send:
		messageSyncEqual(t, m.Origin, protocol.OriginRemote)
	default:
		t.Fatal("ack wake coalesced with continuation; newly free prefix was never revisited")
	}
}
