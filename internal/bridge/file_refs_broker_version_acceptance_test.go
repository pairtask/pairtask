package bridge

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestFileRefsBrokerVersionAcceptanceOldBrokerRefusesBeforeWriting(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 30 // paired claims, before file references
	read := fileRefsReadFrame(remote)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.SendMessage(ctx, fileRefsRequest(t, "paired", []map[string]any{fileRefsSample()}))
	if err == nil {
		t.Fatal("file references were written to a broker that silently drops them")
	}
	if !strings.Contains(err.Error(), "31") {
		t.Fatalf("refusal did not identify the required broker protocol: %v", err)
	}
	_, err = c.SendMessage(ctx, SendRequest{Workspace: "paired", To: "worker", Text: "plain still works", MessageID: "after-file-ref-refusal"})
	requireNoError(t, err)
	assertEqual(t, "old broker received no reference-bearing frame", (<-read).MessageID, "after-file-ref-refusal")
}

func TestFileRefsBrokerVersionAcceptanceOldBrokerStillAcceptsPlainMessage(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 30
	read := fileRefsReadFrame(remote)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.SendMessage(ctx, SendRequest{Workspace: "paired", To: "worker", Text: "plain message", MessageID: "plain-v30"})
	requireNoError(t, err)
	assertEqual(t, "plain send remains available", (<-read).MessageID, "plain-v30")
}

func fileRefsReadFrame(remote net.Conn) <-chan protocol.Message {
	read := make(chan protocol.Message, 1)
	go func() {
		var frame protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(2 * time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &frame)
		read <- frame
	}()
	return read
}
