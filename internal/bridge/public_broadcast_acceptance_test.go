package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestPublicBroadcastAcceptanceOldBrokerFailsBeforeWriting(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 6
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.SendMessage(ctx, SendRequest{To: "*", Text: "public", MessageID: "public"})
	assertErrorContains(t, err, "unsupported public broadcast")
	// A refused public send leaves the transport usable and has written no frame.
	read := make(chan protocol.Message, 1)
	go func() {
		var m protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &m)
		read <- m
	}()
	_, err = c.SendMessage(context.Background(), SendRequest{To: "peer", Text: "private", MessageID: "direct"})
	requireNoError(t, err)
	assertEqual(t, "first wire frame is direct", (<-read).MessageID, "direct")
}

func TestPublicBroadcastAcceptanceEveryOldBrokerNamesUnsupportedAudience(t *testing.T) {
	for _, version := range []int{0, 2, 3, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			c, _ := pipeClient(t)
			c.protocolVersion = version
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := c.SendMessage(ctx, SendRequest{To: "*", Text: "public"})
			assertErrorContains(t, err, "unsupported public broadcast")
		})
	}
}

func TestPublicBroadcastAcceptanceMCPExplainsAudience(t *testing.T) {
	c, _ := pipeClient(t)
	session := connectMCP(t, c)
	listed, err := session.ListTools(context.Background(), nil)
	requireNoError(t, err)
	for _, tool := range listed.Tools {
		if tool.Name != "send" {
			continue
		}
		encoded, err := json.Marshal(tool)
		requireNoError(t, err)
		description := strings.ToLower(string(encoded))
		for _, word := range []string{"public", "operator", "7"} {
			assertEqual(t, "send documents "+word, strings.Contains(description, word), true)
		}
		return
	}
	t.Fatal("send tool missing")
}

func TestPublicBroadcastAcceptanceClientUsesDurablePublicWire(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 7
	read := make(chan protocol.Message, 1)
	go func() {
		var m protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &m)
		read <- m
	}()
	_, err := c.SendMessage(context.Background(), SendRequest{To: "*", Text: "public", MessageID: "public"})
	requireNoError(t, err)
	m := <-read
	assertEqual(t, "public audience", m.To, "*")
	assertEqual(t, "durable", m.Durable, true)
	assertEqual(t, "explicit acknowledgment requested", m.AckRequested, true)
}
