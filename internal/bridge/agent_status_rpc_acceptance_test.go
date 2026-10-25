package bridge

import (
	"bufio"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestAgentStatusRPCCancellationDropsLateErrorWithoutConsumingMessage(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = protocol.AgentStatusVersion
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.ReadStatus(ctx, "peer"); done <- err }()
	var request protocol.Message
	requireNoError(t, remote.SetReadDeadline(time.Now().Add(time.Second)))
	requireNoError(t, protocol.ReadFrame(bufio.NewReader(remote), &request))
	// Wait for the writer to finish its cleanup before canceling the reply
	// wait. Cancellation during a write intentionally closes the transport.
	requireNoError(t, c.acquireWriter(ctx))
	<-c.writeGate
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled RPC: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("status cancellation did not return")
	}
	requireNoError(t, remote.SetWriteDeadline(time.Now().Add(time.Second)))
	requireNoError(t, protocol.WriteFrame(remote, protocol.Message{Type: protocol.TypeError, RequestID: request.RequestID, Code: protocol.ErrInternal}))
	requireNoError(t, protocol.WriteFrame(remote, replyFrame("unrelated", "peer")))
	assertEqual(t, "late RPC error did not enter inbox", nextFrame(t, c).MessageID, "unrelated-reply")
}

func TestAgentStatusRPCConcurrentRepliesAreMatchedByRequest(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = protocol.AgentStatusVersion
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type answer struct {
		wanted string
		status *protocol.AgentStatus
		err    error
	}
	done := make(chan answer, 2)
	for _, id := range []string{"first", "second"} {
		go func(id string) { st, err := c.ReadStatus(ctx, id); done <- answer{id, st, err} }(id)
	}
	replyToStatusRequestsInReverse(t, remote)
	for range 2 {
		select {
		case result := <-done:
			requireNoError(t, result.err)
			if result.status == nil || result.status.AgentID != result.wanted {
				t.Fatalf("reply crossed requests: %+v", result)
			}
		case <-ctx.Done():
			t.Fatal("concurrent status RPC did not return")
		}
	}
}

// A peer deliberately reverses two replies to expose FIFO-based correlation.
func replyToStatusRequestsInReverse(t *testing.T, remote net.Conn) {
	t.Helper()
	r := bufio.NewReader(remote)
	requests := make([]protocol.Message, 2)
	requireNoError(t, remote.SetDeadline(time.Now().Add(time.Second)))
	for i := range requests {
		requireNoError(t, protocol.ReadFrame(r, &requests[i]))
	}
	if requests[0].RequestID == requests[1].RequestID {
		t.Fatal("concurrent requests reused correlation")
	}
	for i := len(requests) - 1; i >= 0; i-- {
		req := requests[i]
		requireNoError(t, protocol.WriteFrame(remote, protocol.Message{Type: "agent_status_result", RequestID: req.RequestID, AgentStatus: &protocol.AgentStatus{AgentID: req.AgentID, State: "active"}}))
	}
}

func TestAgentStatusRPCDuplicateReplyCannotBlockReader(t *testing.T) {
	reply := make(chan protocol.Message, 1)
	c := &Client{pending: map[string]chan protocol.Message{"request": reply}}
	first := protocol.Message{Type: "agent_status_result", RequestID: "request", AgentStatus: &protocol.AgentStatus{State: "active"}}
	if !c.routeResponse(first) {
		t.Fatal("status response not routed")
	}
	done := make(chan struct{})
	go func() { c.routeResponse(first); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		<-reply // Release the blocked route before reporting, avoiding a leaked goroutine.
		<-done
		t.Fatal("duplicate reply blocked the reader behind a full response channel")
	}
	if got := <-reply; got.AgentStatus.State != "active" {
		t.Fatal("first confirmed result changed")
	}
}
