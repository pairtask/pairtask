package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Exercise the real routing transaction with synthetic connected clients.
// Hundreds of sockets would test teardown scheduling instead of the bound.
func TestPublicBroadcastAcceptanceRecipientLimitIncludesOperator(t *testing.T) {
	h := newTestHub(t)
	sender := publicBoundaryClient("sender")
	clients := map[string]*Client{sender.ID: sender}
	for i := range 255 {
		peer := publicBoundaryClient(fmt.Sprintf("agent-%03d", i))
		clients[peer.ID] = peer
	}
	in := Inbound{From: sender, Msg: protocol.Message{
		Type: protocol.TypeMsg, MessageID: "at-limit", To: "*",
		Durable: true, AckRequested: true, Payload: json.RawMessage(`{"text":"public"}`),
	}}
	h.route(context.Background(), clients, in)
	accepted := recv(t, sender)
	if accepted.Stage != protocol.StageAccepted {
		t.Fatalf("256 recipients refused: %+v", accepted)
	}
	if len(accepted.Recipients) != 256 {
		t.Fatalf("recipient count = %d, want 256 including operator", len(accepted.Recipients))
	}
	publicBoundaryInbox(t, h)
	clients["extra"] = publicBoundaryClient("extra")
	in.Msg.MessageID = "over-limit"
	h.route(context.Background(), clients, in)
	refused := recv(t, sender)
	if refused.Code != protocol.ErrRecipientUnavailable {
		t.Fatalf("257 recipients not rejected: %+v", refused)
	}
	if h.seq != accepted.Seq {
		t.Fatalf("overflow consumed sequence: %d, want %d", h.seq, accepted.Seq)
	}
	publicBoundaryInbox(t, h)
}

func publicBoundaryClient(id string) *Client {
	c := newClient(id)
	c.ProtocolVersion = 7
	return c
}

func publicBoundaryInbox(t *testing.T, h *Hub) {
	t.Helper()
	page, err := h.store.ReadOperatorPage(context.Background(), protocol.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("operator inbox has %d messages, want 1", len(page.Messages))
	}
	if page.Messages[0].MessageID != "at-limit" {
		t.Fatalf("unexpected operator message: %+v", page.Messages[0])
	}
}
