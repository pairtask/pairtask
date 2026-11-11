package bridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Agent roles (#339, calibration): the agent declares its own one-line role,
// which the shadow classifier shows its model, and reads how the model's
// picks compared with the replies it claimed, to sharpen the line.

// RoleArgs are set_role's arguments.
type RoleArgs struct {
	Role string `json:"role" jsonschema:"Your role in one line, at most 160 characters: what kind of requests you take (e.g. 'tests, CI, reviews and merge verdicts'); empty clears it"`
}

// RoleResult is set_role's answer.
type RoleResult struct {
	AgentRole protocol.AgentRole `json:"agent_role"`
}

// FeedbackArgs are classifier_feedback's arguments.
type FeedbackArgs struct {
	Limit int `json:"limit,omitempty" jsonschema:"How many recent items to list, 1 to 50; default 20"`
}

// FeedbackResult is classifier_feedback's answer.
type FeedbackResult struct {
	Feedback protocol.ClassifierFeedback `json:"classifier_feedback"`
}

const roleDescription = "Declare your role in one line: the kind of operator requests you take. The shadow classifier (Jev) shows it to its model when it guesses which agent an untagged message to everyone was for. " +
	"Set it when you start and whenever your work changes; sharpen it from classifier_feedback. It routes and assigns nothing."

const feedbackDescription = "How the shadow classifier's guesses compared with the replies you actually claimed: agreed, missed (you claimed it, the model abstained or picked another), wrong_pick, unclaimed_pick, with message IDs, no text. " +
	"Use it to sharpen your role with set_role. Read-only; nothing is applied."

type roleClient interface {
	SetRole(context.Context, string) (protocol.AgentRole, error)
	ClassifierFeedback(context.Context, int) (protocol.ClassifierFeedback, error)
}

func addRoleTools(s *mcp.Server, c roleClient) {
	additive, readOnly := false, true
	mcp.AddTool(s, &mcp.Tool{Name: "set_role", Description: roleDescription, Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive}},
		func(ctx context.Context, _ *mcp.CallToolRequest, args RoleArgs) (*mcp.CallToolResult, RoleResult, error) {
			role, err := c.SetRole(ctx, args.Role)
			return nil, RoleResult{AgentRole: role}, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "classifier_feedback", Description: feedbackDescription, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly}},
		func(ctx context.Context, _ *mcp.CallToolRequest, args FeedbackArgs) (*mcp.CallToolResult, FeedbackResult, error) {
			feedback, err := c.ClassifierFeedback(ctx, args.Limit)
			return nil, FeedbackResult{Feedback: feedback}, err
		})
}

var errRolesUnsupported = errors.New("roles are unsupported by this broker (protocol version 28 required); upgrade the broker")

// SetRole sends role_set and waits for its answer.
func (c *Client) SetRole(ctx context.Context, role string) (protocol.AgentRole, error) {
	out, err := c.roleCall(ctx, protocol.Message{Type: protocol.TypeRoleSet, AgentRoleText: role})
	if err != nil {
		return protocol.AgentRole{}, err
	}
	if out.AgentRole == nil {
		return protocol.AgentRole{}, errors.New("role_result without a role")
	}
	return *out.AgentRole, nil
}

// ClassifierFeedback sends classifier_feedback_get and waits for its answer.
func (c *Client) ClassifierFeedback(ctx context.Context, limit int) (protocol.ClassifierFeedback, error) {
	out, err := c.roleCall(ctx, protocol.Message{Type: protocol.TypeClassifierFeedbackGet, FeedbackLimit: limit})
	if err != nil {
		return protocol.ClassifierFeedback{}, err
	}
	if out.ClassifierFeedback == nil {
		return protocol.ClassifierFeedback{}, errors.New("classifier_feedback_result without feedback")
	}
	return *out.ClassifierFeedback, nil
}

// roleCall sends one correlated role frame; an older broker is refused
// before anything is written.
func (c *Client) roleCall(ctx context.Context, msg protocol.Message) (protocol.Message, error) {
	if c.protocolVersion < protocol.RoleVersion {
		return protocol.Message{}, errRolesUnsupported
	}
	msg.RequestID = uuid.NewString()
	reply, err := c.expect(msg.RequestID)
	if err != nil {
		return protocol.Message{}, err
	}
	defer c.forget(msg.RequestID)
	if err := c.writeFrame(ctx, msg); err != nil {
		return protocol.Message{}, err
	}
	out, err := c.awaitStatus(ctx, reply)
	if err != nil {
		return protocol.Message{}, err
	}
	if out.Type == protocol.TypeError {
		return protocol.Message{}, fmt.Errorf("%s: %s", out.Code, out.Detail)
	}
	return out, nil
}

func (c *LazyClient) SetRole(ctx context.Context, role string) (protocol.AgentRole, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return protocol.AgentRole{}, err
	}
	return client.SetRole(ctx, role)
}

func (c *LazyClient) ClassifierFeedback(ctx context.Context, limit int) (protocol.ClassifierFeedback, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return protocol.ClassifierFeedback{}, err
	}
	return client.ClassifierFeedback(ctx, limit)
}
