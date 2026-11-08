package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func staleBridgeReplacement(t *testing.T) RequestReviewArgs {
	t.Helper()
	data, err := json.Marshal(advisoryBridgeArgs())
	requireNoError(t, err)
	fields := map[string]any{}
	requireNoError(t, json.Unmarshal(data, &fields))
	fields["supersedes"] = "prior"
	data, err = json.Marshal(fields)
	requireNoError(t, err)
	var args RequestReviewArgs
	requireNoError(t, json.Unmarshal(data, &args))
	return args
}

func TestStaleAskRequestBridgeAcceptanceCarriesExplicitReplacement(t *testing.T) {
	c, frames, done := advisoryBridgeTransport(t)
	c.protocolVersion = 26
	_, err := c.RequestReview(context.Background(), staleBridgeReplacement(t))
	requireNoError(t, err)
	requireNoError(t, <-done)
	frame := <-frames
	if frame.ReviewAsk == nil {
		t.Fatal("replacement lost review request")
	}
	assertEqual(t, "supersedes reaches local broker", staleBridgeFields(t, frame.ReviewAsk)["supersedes"], any("prior"))
	assertEqual(t, "new explicit pin retained", frame.ReviewAsk.Head, advisoryBridgeArgs().Head)
}

func TestStaleAskRequestBridgeAcceptanceOldBrokerReceivesNoReplacement(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 25
	first := make(chan protocol.Message, 1)
	go func() {
		var message protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(3 * time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &message)
		first <- message
	}()
	_, err := c.RequestReview(context.Background(), staleBridgeReplacement(t))
	if err == nil {
		t.Fatal("old broker silently received plain ask instead of replacement")
	}
	_, err = c.SendMessage(context.Background(), SendRequest{To: "local", Text: "next message", MessageID: "still-local"})
	requireNoError(t, err)
	assertEqual(t, "first write follows refused replacement", (<-first).MessageID, "still-local")
}
