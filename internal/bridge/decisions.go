package bridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pairtask/pairtask/internal/protocol"
)

// request_decision (#352): the agent asks the broker to record that it
// needs the operator's decision and to tell the operator outside the chat.
// The broker posts the chat message the operator answers (the anchor) as
// this agent, or uses one the agent names; the operator's reply to it
// answers the request. The tool returns the record; delivery of the notice
// is never a decision.

// DecisionArgs are the tool's arguments.
type DecisionArgs struct {
	Summary   string `json:"summary" jsonschema:"One line for the operator, at most 200 characters: what decision is needed"`
	Key       string `json:"key,omitempty" jsonschema:"Your stable key for this request, at most 128 bytes; the same key again returns the first record instead of asking twice"`
	MessageID string `json:"message_id,omitempty" jsonschema:"A durable message you already sent to the operator to hang the request on; empty posts one for you"`
}

const decisionDescription = "Ask the operator for a decision and have them told outside the chat (Slack, when the operator turned it on). " +
	"The broker posts a chat message to the operator for you (or uses one you name), records the request and returns it. " +
	"The operator answers by replying to that message in the chat; nothing else counts, and being notified is not approval. " +
	"Use a stable key so a retry returns the same record instead of asking again."

type decisionClient interface {
	Decide(context.Context, protocol.Message) (protocol.Decision, error)
}

func addDecisionTools(s *mcp.Server, c decisionClient) {
	additive := false
	mcp.AddTool(s, &mcp.Tool{Name: "request_decision", Description: decisionDescription, Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive}},
		func(ctx context.Context, _ *mcp.CallToolRequest, args DecisionArgs) (*mcp.CallToolResult, protocol.Decision, error) {
			out, err := c.Decide(ctx, protocol.Message{Type: protocol.TypeDecisionRequest, DecisionSummary: args.Summary,
				DecisionKey: args.Key, AnchorMessageID: args.MessageID})
			return nil, out, err
		})
}

var errDecisionsUnsupported = errors.New("decision requests are unsupported by this broker (protocol version 27 required); upgrade the broker")

// Decide sends one request frame and waits for its correlated answer.
func (c *Client) Decide(ctx context.Context, msg protocol.Message) (protocol.Decision, error) {
	if c.protocolVersion < protocol.DecisionVersion {
		return protocol.Decision{}, errDecisionsUnsupported
	}
	msg.RequestID = uuid.NewString()
	reply, err := c.expect(msg.RequestID)
	if err != nil {
		return protocol.Decision{}, err
	}
	defer c.forget(msg.RequestID)
	if err := c.writeFrame(ctx, msg); err != nil {
		return protocol.Decision{}, err
	}
	out, err := c.awaitStatus(ctx, reply)
	if err != nil {
		return protocol.Decision{}, err
	}
	if out.Type == protocol.TypeError {
		return protocol.Decision{}, fmt.Errorf("%s: %s", out.Code, out.Detail)
	}
	if out.Decision == nil {
		return protocol.Decision{}, errors.New("decision_result without a decision")
	}
	return *out.Decision, nil
}

func (c *LazyClient) Decide(ctx context.Context, msg protocol.Message) (protocol.Decision, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return protocol.Decision{}, err
	}
	return client.Decide(ctx, msg)
}
