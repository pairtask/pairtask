package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func roomSendRequest(t *testing.T) SendRequest {
	t.Helper()
	var req SendRequest
	requireNoError(t, json.Unmarshal([]byte(`{"to":"*","text":"Square discussion","message_id":"scoped","room":"alpha"}`), &req))
	return req
}

func TestRoomsBridgeAcceptanceOldBrokerRefusesScopeBeforeWriting(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 8
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.SendMessage(ctx, roomSendRequest(t))
	assertErrorContains(t, err, "unsupported")
	read := make(chan protocol.Message, 1)
	go func() {
		var m protocol.Message
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_ = protocol.ReadFrame(bufio.NewReader(remote), &m)
		read <- m
	}()
	_, err = c.SendMessage(context.Background(), SendRequest{To: "peer", Text: "still usable", MessageID: "after-refusal"})
	requireNoError(t, err)
	assertEqual(t, "first frame after local refusal", (<-read).MessageID, "after-refusal")
}

func TestRoomsBridgeAcceptanceMCPSendPreservesScope(t *testing.T) {
	c, remote := pipeClient(t)
	c.protocolVersion = 9
	session := connectMCP(t, c)
	read := make(chan map[string]any, 1)
	go func() {
		var m map[string]any
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_ = json.NewDecoder(bufio.NewReader(remote)).Decode(&m)
		read <- m
	}()
	call(t, session, "send", map[string]any{"to": "*", "text": "Square discussion", "message_id": "scoped", "room": "square"})
	m := <-read
	assertEqual(t, "explicit scope retained", m["room"], any("square"))
	assertEqual(t, "broadcast audience retained", m["to"], any("*"))
	assertEqual(t, "durable room delivery", m["durable"], any(true))
}

func TestRoomsBridgeAcceptanceMembershipToolsUseCurrentBrokerState(t *testing.T) {
	path, _ := startBroker(t)
	session := connectMCP(t, lazyAgent(t, path, "room-worker"))
	check := func(name string, args map[string]any, want string) {
		t.Helper()
		result := call(t, session, name, args)
		data, err := json.Marshal(result.StructuredContent)
		requireNoError(t, err)
		var value struct {
			Rooms []string `json:"rooms"`
		}
		requireNoError(t, json.Unmarshal(data, &value))
		assertEqual(t, "memberships", strings.Join(value.Rooms, ","), want)
	}
	check("join_room", map[string]any{"room": "alpha"}, "alpha")
	check("join_room", map[string]any{"room": "beta"}, "alpha,beta")
	check("leave_room", map[string]any{"room": "alpha"}, "beta")
	check("list_rooms", map[string]any{}, "beta")
	inbox := inboxResult(t, call(t, session, "receive", map[string]any{}))
	assertEqual(t, "room RPC responses not inbox messages", len(inbox.Messages), 0)
}
