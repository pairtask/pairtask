package bridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

// ClaimArgs names one task of an operator message.
type ClaimArgs struct {
	OperatorMessageID string `json:"operator_message_id" jsonschema:"ID of the operator message the task belongs to"`
	Task              string `json:"task" jsonschema:"Stable task key: 1 to 64 of a-z, 0-9 and -; use reply before answering an operator message sent to everyone"`
	Topic             string `json:"topic,omitempty" jsonschema:"Short human label for the task, at most 120 bytes"`
	Role              string `json:"role,omitempty" jsonschema:"owner (default) or reviewer; a reviewer claims the role itself"`
}

// ClaimsResult is an operator message's claims after a claim call.
type ClaimsResult struct {
	OperatorMessageID string           `json:"operator_message_id"`
	Claims            []protocol.Claim `json:"claims"`
	Total             int              `json:"claims_total"`
	Truncated         bool             `json:"claims_truncated"`
}

type claimClient interface {
	Claim(context.Context, protocol.Message) (ClaimsResult, error)
}

const claimsMeaning = " A claim coordinates who does what among agents addressed together; it is never an approval, an authorization or a file lock."

func addClaimTools(s *mcp.Server, c claimClient) {
	mcp.AddTool(s, &mcp.Tool{Name: "claim", Description: "Claim one task of an operator message, as owner (default) or as reviewer, before doing it. Before answering an operator message sent to everyone, claim task reply; if the broker answers claim_conflict, another agent owns it: don't answer, unless to object. The first claim wins; repeating your own claim is harmless." + claimsMeaning},
		claimTool(c, protocol.TypeClaimSet))
	mcp.AddTool(s, &mcp.Tool{Name: "release_claim", Description: "Free your claim so another agent can take the task. Only the holder can release; disconnecting never releases." + claimsMeaning},
		claimTool(c, protocol.TypeClaimRelease))
	mcp.AddTool(s, &mcp.Tool{Name: "finish_claim", Description: "Mark your owned task done. Done is final and stays visible; the task can't be claimed again." + claimsMeaning},
		claimTool(c, protocol.TypeClaimDone))
	mcp.AddTool(s, &mcp.Tool{Name: "read_claims", Description: "Read the claims on an operator message: who owns and who reviews each task, and their state." + claimsMeaning},
		func(ctx context.Context, _ *mcp.CallToolRequest, args struct {
			OperatorMessageID string `json:"operator_message_id" jsonschema:"ID of the operator message"`
		}) (*mcp.CallToolResult, ClaimsResult, error) {
			out, err := c.Claim(ctx, protocol.Message{Type: protocol.TypeClaimGet, OperatorMessageID: args.OperatorMessageID})
			return nil, out, err
		})
}

func claimTool(c claimClient, kind string) func(context.Context, *mcp.CallToolRequest, ClaimArgs) (*mcp.CallToolResult, ClaimsResult, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args ClaimArgs) (*mcp.CallToolResult, ClaimsResult, error) {
		role := args.Role
		if role == "" && kind != protocol.TypeClaimDone {
			role = protocol.RoleOwner
		}
		out, err := c.Claim(ctx, protocol.Message{Type: kind, OperatorMessageID: args.OperatorMessageID, Task: args.Task, Topic: args.Topic, Role: role})
		return nil, out, err
	}
}

var errClaimsUnsupported = errors.New("claims are unsupported by this broker (protocol version 8 required); upgrade the broker")

// Claim sends one claim frame and waits for its correlated answer.
func (c *Client) Claim(ctx context.Context, msg protocol.Message) (ClaimsResult, error) {
	if c.protocolVersion < protocol.ClaimVersion {
		return ClaimsResult{}, errClaimsUnsupported
	}
	msg.RequestID = uuid.NewString()
	reply, err := c.expect(msg.RequestID)
	if err != nil {
		return ClaimsResult{}, err
	}
	defer c.forget(msg.RequestID)
	if err := c.writeFrame(ctx, msg); err != nil {
		return ClaimsResult{}, err
	}
	out, err := c.awaitStatus(ctx, reply)
	if err != nil {
		return ClaimsResult{}, err
	}
	return claimAnswer(out)
}

// claimAnswer turns a claim_result into the tool result; a refusal becomes an
// error that names the holder when there is one.
func claimAnswer(out protocol.Message) (ClaimsResult, error) {
	result := ClaimsResult{OperatorMessageID: out.OperatorMessageID, Claims: []protocol.Claim{}}
	if out.ClaimPage != nil {
		result.Claims, result.Total, result.Truncated = out.Claims, out.Total, out.Truncated
	}
	if out.Type != protocol.TypeError {
		return result, nil
	}
	if out.Code == protocol.ErrClaimConflict {
		return result, fmt.Errorf("%s: %s%s", out.Code, out.Detail, holders(result.Claims))
	}
	return result, fmt.Errorf("%s: %s", out.Code, out.Detail)
}

// holders names the agents with an active or done claim, for a conflict.
func holders(claims []protocol.Claim) string {
	text := ""
	for _, c := range claims {
		if c.State != protocol.ClaimReleased {
			text += fmt.Sprintf("; %s: %s (%s, %s)", c.Task, c.AgentID, c.Role, c.State)
		}
	}
	return text
}

func (c *LazyClient) Claim(ctx context.Context, msg protocol.Message) (ClaimsResult, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return ClaimsResult{}, err
	}
	return client.Claim(ctx, msg)
}
