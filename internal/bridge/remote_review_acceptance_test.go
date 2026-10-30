package bridge

import (
	"bufio"
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestRemoteReviewBridgeAcceptanceOldBrokerWritesNothing(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 13
	_, err := c.RequestReview(context.Background(), RequestReviewArgs{Workspace: "paired", To: "reviewer", Target: "pairtask/pairtask#235", Base: "base", Head: "head"})
	assertErrorContains(t, err, "protocol version 14")
	read := make(chan protocol.Message, 1)
	go func() {
		var m protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &m)
		read <- m
	}()
	_, err = c.SendMessage(context.Background(), SendRequest{To: "local", Text: "still local", MessageID: "local"})
	requireNoError(t, err)
	assertEqual(t, "first written frame", (<-read).MessageID, "local")
}

func TestRemoteReviewBridgeAcceptancePinsPolicyAndIdentity(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 14
	read := make(chan protocol.Message, 1)
	go func() {
		var m protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &m)
		read <- m
	}()
	args := RequestReviewArgs{Workspace: "workspace with spaces", To: "reviewer/@\nname", Target: "pairtask/pairtask#235", Base: "base", Head: "head", Formal: true, MessageID: "stable"}
	_, err := c.RequestReview(context.Background(), args)
	requireNoError(t, err)
	got := <-read
	if got.ReviewAsk == nil {
		t.Fatal("request lost ask")
	}
	assertEqual(t, "workspace", got.Workspace, args.Workspace)
	assertEqual(t, "verbatim reviewer", got.To, args.To)
	assertEqual(t, "stable ID", got.MessageID, args.MessageID)
	assertEqual(t, "base", got.ReviewAsk.Base, args.Base)
	assertEqual(t, "head", got.ReviewAsk.Head, args.Head)
	assertEqual(t, "no default publish", got.ReviewAsk.Publish, false)
	assertEqual(t, "formal requires publish", got.ReviewAsk.Formal, false)
	assertEqual(t, "durable", got.Durable, true)
}
