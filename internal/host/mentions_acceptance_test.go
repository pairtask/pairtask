package host

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestMentionsAcceptanceCodexTurnCarriesAttention(t *testing.T) {
	p, up, down := proxyFixture(t)
	startThread(t, p)
	frameAt(t, up)
	frameAt(t, down)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := protocol.Message{Type: protocol.TypeMsg, From: "operator", Origin: protocol.OriginOperator,
		To: "*", Room: "alpha", MessageID: "attention-turn", Mentions: []string{"worker"}, AckRequested: true,
		Payload: json.RawMessage(`{"text":"@worker please review"}`)}
	done := make(chan error, 1)
	go func() { done <- p.Publish(ctx, input) }()
	request := frameAt(t, up)
	if request.Method != "turn/start" {
		t.Fatalf("wrong method: %s", request.Method)
	}
	var params struct {
		ToolOutput struct{ Output string } `json:"toolOutput"`
	}
	check(t, json.Unmarshal(request.Params, &params))
	var delivered protocol.Message
	check(t, json.Unmarshal([]byte(params.ToolOutput.Output), &delivered))
	if !slices.Equal(delivered.Mentions, input.Mentions) || delivered.Room != input.Room || delivered.Origin != input.Origin || !delivered.AckRequested {
		t.Fatalf("Codex turn lost attention or authority: %+v", delivered)
	}
	check(t, p.FromHost(ctx, Frame{ID: request.ID, Result: json.RawMessage(`{"turn":{"id":"attention-turn"}}`)}))
	check(t, <-done)
}
