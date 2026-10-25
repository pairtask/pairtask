package bridge

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func assertEqual[T comparable](t *testing.T, field string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", field, got, want)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
}

func nextFrame(t *testing.T, c *Client) protocol.Message {
	t.Helper()
	out, err := c.Receive(context.Background(), 1, time.Second)
	requireNoError(t, err)
	assertEqual(t, "frame count", len(out.Messages), 1)
	return out.Messages[0]
}

func sendTracked(t *testing.T, c *Client, request SendRequest) SendResult {
	t.Helper()
	result, err := c.SendMessage(context.Background(), request)
	requireNoError(t, err)
	return result
}

func assertInboxTimeout(t *testing.T, c *Client) {
	t.Helper()
	out, err := c.Receive(context.Background(), 20, 20*time.Millisecond)
	requireNoError(t, err)
	assertEqual(t, "timed out", out.TimedOut, true)
	assertEqual(t, "unexpected frames", len(out.Messages), 0)
}

func assertOwnerRejected(t *testing.T, rejected *LazyClient, owner *Client) {
	t.Helper()
	_, err := rejected.Receive(context.Background(), 20, 0)
	assertErrorContains(t, err, owner.sessionID)
	assertErrorContains(t, err, protocol.ErrDuplicateID)
}

func registerAfterRelease(t *testing.T, c *LazyClient) Inbox {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		out, err := c.Receive(context.Background(), 20, 0)
		if err == nil {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDuplicateMessagingSessionCannotEvictOwner(t *testing.T) {
	path, _ := startBroker(t)
	owner := connectAgent(t, path, "codex")
	peer := connectAgent(t, path, "claude")
	rejected := lazyAgent(t, path, "codex")
	assertOwnerRejected(t, rejected, owner)
	assertOwnerRejected(t, rejected, owner)
	requireNoError(t, peer.Send(context.Background(), "codex", "owner is still here"))
	assertEqual(t, "sender", nextFrame(t, owner).From, "claude")
	oldSession := owner.sessionID
	owner.Close()
	out := registerAfterRelease(t, rejected)
	assertEqual(t, "connected", out.Connected, true)
	assertEqual(t, "missing session", out.SessionID == "", false)
	assertEqual(t, "reused session", out.SessionID == oldSession, false)
	requireNoError(t, peer.Send(context.Background(), "codex", "new owner"))
	out, err := rejected.Receive(context.Background(), 20, time.Second)
	requireNoError(t, err)
	assertSinglePayload(t, out, `{"text":"new owner"}`)
}

type broadcastReview struct {
	path       string
	sender     *Client
	recipients []*Client
	id         string
}

func startBroadcastReview(t *testing.T) broadcastReview {
	t.Helper()
	path, _ := startBroker(t)
	b := broadcastReview{path: path, sender: connectAgent(t, path, "claude"), recipients: []*Client{connectAgent(t, path, "codex"), connectAgent(t, path, "reviewer")}}
	b.id = sendTracked(t, b.sender, SendRequest{To: "*", Text: "review", MessageID: "broadcast-review"}).MessageID
	accepted := nextFrame(t, b.sender)
	assertStage(t, accepted, b.id, protocol.StageAccepted)
	// v7: the public broadcast also freezes the operator inbox (no session shown).
	want := []protocol.Recipient{{AgentID: "codex", SessionID: b.recipients[0].sessionID}, {AgentID: "operator"}, {AgentID: "reviewer", SessionID: b.recipients[1].sessionID}}
	if !reflect.DeepEqual(accepted.Recipients, want) {
		t.Fatalf("broadcast scope = %+v, want %+v", accepted.Recipients, want)
	}
	return b
}

func (b broadcastReview) receive(t *testing.T) {
	t.Helper()
	for _, c := range b.recipients {
		msg := nextFrame(t, c)
		assertEqual(t, "broadcast message ID", msg.MessageID, b.id)
		assertEqual(t, "broadcast recipient", msg.To, "*")
	}
	seen := map[string]bool{}
	for range b.recipients {
		receipt := nextFrame(t, b.sender)
		assertStage(t, receipt, b.id, protocol.StageAdapterReceived)
		seen[receipt.AgentID] = true
	}
	assertEqual(t, "codex receipt", seen["codex"], true)
	assertEqual(t, "reviewer receipt", seen["reviewer"], true)
}

func TestBroadcastAcknowledgmentsFreezeMembership(t *testing.T) {
	b := startBroadcastReview(t)
	late := connectAgent(t, b.path, "late")
	b.receive(t)
	requireNoError(t, late.Acknowledge(context.Background(), b.id))
	assertEqual(t, "late peer acknowledgment", nextFrame(t, late).Code, protocol.ErrInvalidAck)
}

func TestDuplicateBroadcastIsNotRoutedAgain(t *testing.T) {
	b := startBroadcastReview(t)
	b.receive(t)
	sendTracked(t, b.sender, SendRequest{To: "*", Text: "changed content", MessageID: b.id})
	rejected := nextFrame(t, b.sender)
	assertEqual(t, "duplicate error", rejected.Code, protocol.ErrDuplicateMessage)
	assertEqual(t, "rejected message ID", rejected.MessageID, b.id)
	for _, c := range b.recipients {
		assertInboxTimeout(t, c)
	}
}

type wireAgent struct {
	conn    net.Conn
	reader  *bufio.Reader
	welcome protocol.Message
}

func connectWireAgent(t *testing.T, path string, hello protocol.Message) *wireAgent {
	t.Helper()
	conn, err := net.Dial("unix", path)
	requireNoError(t, err)
	t.Cleanup(func() { conn.Close() })
	requireNoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
	w := &wireAgent{conn: conn, reader: bufio.NewReader(conn)}
	w.send(t, hello)
	w.welcome = w.next(t)
	assertEqual(t, "welcome type", w.welcome.Type, protocol.TypeWelcome)
	return w
}

func (w *wireAgent) send(t *testing.T, msg protocol.Message) {
	t.Helper()
	requireNoError(t, protocol.WriteFrame(w.conn, msg))
}

func (w *wireAgent) next(t *testing.T) protocol.Message {
	t.Helper()
	var msg protocol.Message
	requireNoError(t, protocol.ReadFrame(w.reader, &msg))
	return msg
}

func TestRecipientDisconnectBetweenAcknowledgmentStages(t *testing.T) {
	for name, receipt := range map[string]bool{"before-adapter-receipt": false, "after-adapter-receipt": true} {
		t.Run(name, func(t *testing.T) { testReceiptDisconnect(t, receipt) })
	}
}

func testReceiptDisconnect(t *testing.T, receipt bool) {
	t.Helper()
	path, _ := startBroker(t)
	sender := connectAgent(t, path, "claude")
	recipient := connectWireAgent(t, path, protocol.Message{Type: protocol.TypeHello, AgentID: "codex", ProtocolVersion: protocol.Version})
	sent := sendTracked(t, sender, SendRequest{To: "codex", Text: "review", MessageID: "disconnect-review", NonDurable: true})
	assertStage(t, nextFrame(t, sender), sent.MessageID, protocol.StageAccepted)
	assertEqual(t, "message ID", recipient.next(t).MessageID, sent.MessageID)
	if receipt {
		recipient.send(t, protocol.Message{Type: protocol.TypeAck, MessageID: sent.MessageID, Stage: protocol.StageAdapterReceived})
		assertStage(t, nextFrame(t, sender), sent.MessageID, protocol.StageAdapterReceived)
	}
	recipient.conn.Close()
	got := nextFrame(t, sender)
	assertEqual(t, "disconnect error", got.Code, protocol.ErrRecipientDisconnected)
	assertEqual(t, "disconnected message ID", got.MessageID, sent.MessageID)
	assertEqual(t, "disconnected session", got.SessionID, recipient.welcome.SessionID)
	newOwner := connectAgent(t, path, "codex")
	requireNoError(t, newOwner.Acknowledge(context.Background(), sent.MessageID))
	assertEqual(t, "new session acknowledgment", nextFrame(t, newOwner).Code, protocol.ErrInvalidAck)
}

func TestLegacyRecipientDoesNotImplyReceipt(t *testing.T) {
	path, _ := startBroker(t)
	sender := connectAgent(t, path, "claude")
	legacy := connectWireAgent(t, path, protocol.Message{Type: protocol.TypeHello, AgentID: "legacy"})
	legacy.send(t, protocol.Message{Type: protocol.TypeMsg, To: "claude"})
	got := nextFrame(t, sender)
	assertEqual(t, "legacy message type", got.Type, protocol.TypeMsg)
	assertEqual(t, "missing legacy message ID", got.MessageID == "", false)
	assertEqual(t, "legacy ack requested", got.AckRequested, false)
	sent := sendTracked(t, sender, SendRequest{To: "legacy", Text: "review", NonDurable: true})
	assertStage(t, nextFrame(t, sender), sent.MessageID, protocol.StageAccepted)
	msg := legacy.next(t)
	assertEqual(t, "message type", msg.Type, protocol.TypeMsg)
	assertEqual(t, "message ID", msg.MessageID, sent.MessageID)
	assertInboxTimeout(t, sender)
}

func TestOldBrokerCapabilitiesAreExplicit(t *testing.T) {
	c, _ := pipeClient(t)
	out, err := c.Receive(context.Background(), 20, 0)
	requireNoError(t, err)
	assertEqual(t, "acknowledgments supported", out.AcknowledgmentsSupported, false)
	_, err = c.SendMessage(context.Background(), SendRequest{To: "peer", Text: "review"})
	assertErrorContains(t, err, "upgrade")
	assertErrorContains(t, c.Acknowledge(context.Background(), "review"), "upgrade")
}

func TestWelcomeReportsNegotiatedProtocolVersion(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3, 4} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path, _ := startBroker(t)
			wire := connectWireAgent(t, path, protocol.Message{Type: protocol.TypeHello, AgentID: "peer", ProtocolVersion: version})
			assertEqual(t, "negotiated protocol version", wire.welcome.ProtocolVersion, min(version, protocol.Version))
			wire.send(t, protocol.Message{Type: protocol.TypeMsg, To: "peer", MessageID: "negotiated", AckRequested: true})
			if version < protocol.AcknowledgmentVersion {
				assertEqual(t, "unsupported tracked send", wire.next(t).Code, protocol.ErrMalformedFrame)
				return
			}
			assertStage(t, wire.next(t), "negotiated", protocol.StageAccepted)
			assertEqual(t, "tracked message", wire.next(t).MessageID, "negotiated")
		})
	}
}
