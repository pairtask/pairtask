package bridge

import (
	"bufio"
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReviewOutcomeBridgeAcceptanceOldBrokerWritesNothing(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 14
	_, err := c.ReviewsAsked(context.Background())
	assertErrorContains(t, err, "protocol version 15")
	read := make(chan protocol.Message, 1)
	go func() {
		var m protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &m)
		read <- m
	}()
	_, err = c.SendMessage(context.Background(), SendRequest{To: "local", Text: "local", MessageID: "local"})
	requireNoError(t, err)
	assertEqual(t, "first frame", (<-read).MessageID, "local")
}
