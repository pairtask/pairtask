package bridge

import (
	"bufio"
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestWorkspaceBridgeAcceptanceOldBrokerWritesNothing(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 12
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.SendMessage(ctx, SendRequest{To: "agent", Workspace: "paired", Text: "remote", MessageID: "remote"})
	assertErrorContains(t, err, "protocol version 13")
	_, err = c.Workspaces(ctx)
	assertErrorContains(t, err, "protocol version 13")
	read := make(chan protocol.Message, 1)
	go func() {
		var m protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &m)
		read <- m
	}()
	_, err = c.SendMessage(context.Background(), SendRequest{To: "local", Text: "local", MessageID: "local"})
	requireNoError(t, err)
	assertEqual(t, "first frame remains local", (<-read).MessageID, "local")
}

func TestWorkspaceBridgeAcceptancePreservesStructuredAddress(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 13
	read := make(chan protocol.Message, 1)
	go func() {
		var m protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &m)
		read <- m
	}()
	request := SendRequest{To: "remote:agent/@\nname", Workspace: "workspace with spaces", Text: "hello", MessageID: "start"}
	_, err := c.SendMessage(context.Background(), request)
	requireNoError(t, err)
	got := <-read
	assertEqual(t, "workspace separate", got.Workspace, request.Workspace)
	assertEqual(t, "verbatim target", got.To, request.To)
	assertEqual(t, "durable", got.Durable, true)
	assertEqual(t, "explicit ack", got.AckRequested, true)
}
