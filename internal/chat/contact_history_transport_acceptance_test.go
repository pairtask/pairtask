package chat

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestContactHistoryChatAcceptanceNegotiatesProtocol26(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	received := make(chan protocol.Message, 1)
	serverErr := make(chan error, 1)
	go func() {
		var req protocol.Message
		if err := protocol.ReadFrame(bufio.NewReader(server), &req); err != nil {
			serverErr <- err
			return
		}
		received <- req
		serverErr <- protocol.WriteFrame(server, protocol.Message{Type: protocol.TypeOperatorSession, RequestID: "open", Session: &protocol.SessionInfo{SessionID: "history-session", Epoch: "history-broker", Capabilities: protocol.SessionCapabilities}})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	transport, _, err := openSession(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	req := <-received
	if req.ProtocolVersion != 26 {
		t.Fatalf("session_open protocol_version=%d, want 26", req.ProtocolVersion)
	}
	if protocol.Version != 26 {
		t.Fatalf("current protocol=%d, want 26", protocol.Version)
	}
	if protocol.ConsentVersion != 24 {
		t.Fatalf("consent compatibility version changed to %d", protocol.ConsentVersion)
	}
}
