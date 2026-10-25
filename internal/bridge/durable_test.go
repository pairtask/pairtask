package bridge

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/server"
	"github.com/pairtask/pairtask/internal/store"
)

// A real child broker is killed without cleanup, exercising SQLite crash
// recovery rather than substituting a graceful hub shutdown for a crash.
func TestDurableBrokerProcess(t *testing.T) {
	socket := os.Getenv("PAIRTASK_DURABLE_TEST_SOCKET")
	if socket == "" {
		return
	}
	st, err := store.Open(socket + ".db")
	requireNoError(t, err)
	seq, err := st.LastSeq(context.Background())
	requireNoError(t, err)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(st, seq, logger)
	go h.Run(context.Background())
	requireNoError(t, server.New(socket, h, logger).Listen(context.Background()))
}

func startCrashBroker(t *testing.T, socket string) func() {
	t.Helper()
	exe, err := os.Executable()
	requireNoError(t, err)
	cmd := exec.Command(exe, "-test.run=^TestDurableBrokerProcess$")
	cmd.Env = append(os.Environ(), "PAIRTASK_DURABLE_TEST_SOCKET="+socket)
	requireNoError(t, cmd.Start())
	var once sync.Once
	stop := func() { once.Do(func() { cmd.Process.Kill(); cmd.Wait(); os.Remove(socket) }) }
	t.Cleanup(stop)
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return stop
		}
		if time.Now().After(deadline) {
			t.Fatal("test broker did not start:", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func crashSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "collab-durable-test-")
	requireNoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "broker.sock")
}

func TestDurableBrokerCrashAndBothAdaptersRestart(t *testing.T) {
	socket := crashSocket(t)
	stop := startCrashBroker(t, socket)
	sender := connectAgent(t, socket, "sender")
	recipient := connectAgent(t, socket, "recipient")
	request := SendRequest{To: "recipient", Text: "review crash recovery", MessageID: "crash-review", InReplyTo: "context"}
	sendTracked(t, sender, request)
	accepted := nextFrame(t, sender)
	assertStage(t, accepted, request.MessageID, protocol.StageAccepted)
	assertEqual(t, "durable acceptance", accepted.Durable, true)
	first := nextFrame(t, recipient)
	assertStage(t, nextFrame(t, sender), request.MessageID, protocol.StageAdapterReceived)
	stop()
	sender.Close()
	recipient.Close()
	startCrashBroker(t, socket)
	recovered := connectAgent(t, socket, "recipient")
	replay := nextFrame(t, recovered)
	assertEqual(t, "replayed", replay.Replayed, true)
	assertEqual(t, "original ID", replay.MessageID, first.MessageID)
	assertEqual(t, "original sequence", replay.Seq, first.Seq)
	assertEqual(t, "original sender session", replay.SessionID, first.SessionID)
	assertEqual(t, "reply correlation", replay.InReplyTo, "context")
	requireNoError(t, recovered.Acknowledge(context.Background(), request.MessageID))
	assertStage(t, nextFrame(t, recovered), request.MessageID, protocol.StageAgentAcknowledged)
	newSender := connectAgent(t, socket, "sender")
	sendTracked(t, newSender, request)
	retry := nextFrame(t, newSender)
	assertEqual(t, "retry recognized", retry.Repeated, true)
	assertEqual(t, "original membership", retry.Recipients[0], accepted.Recipients[0])
	assertEqual(t, "completed outcome", retry.Receipts[0].Stage, protocol.StageAgentAcknowledged)
	assertInboxTimeout(t, recovered)
}

func TestDurableOfflineAndPendingReplay(t *testing.T) {
	socket, _ := startBroker(t)
	recipient := connectAgent(t, socket, "recipient")
	recipient.Close()
	sender := connectAgent(t, socket, "sender")
	sendTracked(t, sender, SendRequest{To: "recipient", Text: "offline context", MessageID: "offline"})
	assertStage(t, nextFrame(t, sender), "offline", protocol.StageAccepted)
	recovered := connectAgent(t, socket, "recipient")
	msg := nextFrame(t, recovered)
	assertEqual(t, "offline ID", msg.MessageID, "offline")
	assertEqual(t, "offline recovery", msg.Replayed, true)
	assertStage(t, nextFrame(t, sender), "offline", protocol.StageAdapterReceived)
	// Host exposure without agent acknowledgment must still replay next time.
	recovered.Close()
	assertEqual(t, "disconnect", nextFrame(t, sender).Code, protocol.ErrRecipientDisconnected)
	again := connectAgent(t, socket, "recipient")
	assertEqual(t, "still pending", nextFrame(t, again).MessageID, "offline")
}

func TestLostAcknowledgmentConfirmationDoesNotReplay(t *testing.T) {
	socket := crashSocket(t)
	stop := startCrashBroker(t, socket)
	sender := connectAgent(t, socket, "sender")
	wire := connectWireAgent(t, socket, protocol.Message{Type: protocol.TypeHello, AgentID: "recipient", ProtocolVersion: protocol.Version})
	sendTracked(t, sender, SendRequest{To: "recipient", Text: "context", MessageID: "acked"})
	assertStage(t, nextFrame(t, sender), "acked", protocol.StageAccepted)
	assertEqual(t, "delivery", wire.next(t).MessageID, "acked")
	wire.send(t, protocol.Message{Type: protocol.TypeAck, MessageID: "acked", Stage: protocol.StageAdapterReceived})
	assertStage(t, nextFrame(t, sender), "acked", protocol.StageAdapterReceived)
	wire.send(t, protocol.Message{Type: protocol.TypeAck, MessageID: "acked", Stage: protocol.StageAgentAcknowledged})
	assertStage(t, nextFrame(t, sender), "acked", protocol.StageAgentAcknowledged)
	// The recipient never consumes its confirmation. Kill the broker now.
	stop()
	sender.Close()
	wire.conn.Close()
	startCrashBroker(t, socket)
	recovered := connectAgent(t, socket, "recipient")
	assertInboxTimeout(t, recovered)
	requireNoError(t, recovered.Acknowledge(context.Background(), "acked"))
	assertStage(t, nextFrame(t, recovered), "acked", protocol.StageAgentAcknowledged)
}

func TestHostUnavailableThenRecoveredListener(t *testing.T) {
	socket, _ := startBroker(t)
	broken := NewListener(context.Background(), lazyAgent(t, socket, "worker"), &testPublisher{err: errors.New("host unavailable")})
	t.Cleanup(broken.Close)
	_, err := broken.Activate(context.Background())
	requireNoError(t, err)
	sender := connectAgent(t, socket, "sender")
	sendTracked(t, sender, SendRequest{To: "worker", Text: "review", MessageID: "host-retry"})
	select {
	case <-broken.done:
	case <-time.After(2 * time.Second):
		t.Fatal("host failure did not stop listener")
	}
	awaitRecipientDisconnect(t, sender)
	host := &testPublisher{messages: make(chan protocol.Message, 1)}
	recovered := NewListener(context.Background(), lazyAgent(t, socket, "worker"), host)
	t.Cleanup(recovered.Close)
	_, err = recovered.Activate(context.Background())
	requireNoError(t, err)
	msg := awaitPublished(t, host)
	assertEqual(t, "recovered host message", msg.MessageID, "host-retry")
	assertEqual(t, "marked replay", msg.Replayed, true)
	requireNoError(t, recovered.Acknowledge(context.Background(), msg.MessageID))
}

func awaitRecipientDisconnect(t *testing.T, sender *Client) {
	t.Helper()
	for range 3 {
		if nextFrame(t, sender).Code == protocol.ErrRecipientDisconnected {
			return
		}
	}
	t.Fatal("broker did not release failed host ownership")
}

func TestDurableRequiresUpgradedRecipient(t *testing.T) {
	socket, _ := startBroker(t)
	sender := connectAgent(t, socket, "sender")
	connectWireAgent(t, socket, protocol.Message{Type: protocol.TypeHello, AgentID: "legacy", ProtocolVersion: 2})
	sendTracked(t, sender, SendRequest{To: "legacy", Text: "review"})
	assertEqual(t, "incompatible recipient", nextFrame(t, sender).Code, protocol.ErrDurabilityUnavailable)
	sendTracked(t, sender, SendRequest{To: "*", Text: "review"})
	assertEqual(t, "broadcast not silently narrowed", nextFrame(t, sender).Code, protocol.ErrDurabilityUnavailable)
}

func TestDurableBroadcastRetryDoesNotExpandMembership(t *testing.T) {
	b := startBroadcastReview(t)
	b.receive(t)
	b.recipients[0].Close()
	assertEqual(t, "disconnected member", nextFrame(t, b.sender).Code, protocol.ErrRecipientDisconnected)
	late := connectAgent(t, b.path, "late")
	sendTracked(t, b.sender, SendRequest{To: "*", Text: "review", MessageID: b.id})
	retry := nextFrame(t, b.sender)
	assertEqual(t, "broadcast retry", retry.Repeated, true)
	assertEqual(t, "original scope (two peers and the operator)", len(retry.Recipients), 3)
	assertInboxTimeout(t, late)
	assertInboxTimeout(t, b.recipients[1])
	recovered := connectAgent(t, b.path, "codex")
	assertEqual(t, "original member replay", nextFrame(t, recovered).MessageID, b.id)
}

func TestVersionTwoBrokerNeedsExplicitNonDurableSend(t *testing.T) {
	c, peer := pipeClient(t)
	c.protocolVersion = 2
	out, err := c.Receive(context.Background(), 1, 0)
	requireNoError(t, err)
	assertEqual(t, "v2 acknowledgments", out.AcknowledgmentsSupported, true)
	assertEqual(t, "v2 durability", out.DurabilitySupported, false)
	_, err = c.SendMessage(context.Background(), SendRequest{To: "peer", Text: "review"})
	assertErrorContains(t, err, "non_durable")
	done := make(chan error, 1)
	go func() {
		_, err := c.SendMessage(context.Background(), SendRequest{To: "peer", Text: "review", NonDurable: true})
		done <- err
	}()
	var msg protocol.Message
	requireNoError(t, protocol.ReadFrame(bufio.NewReader(peer), &msg))
	requireNoError(t, <-done)
	assertEqual(t, "explicit legacy mode", msg.Durable, false)
}
