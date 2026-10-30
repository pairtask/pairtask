package bridge

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/review"
)

// RequestReviewArgs ask a paired workspace's agent for a review (#235).
type RequestReviewArgs struct {
	Workspace string `json:"workspace" jsonschema:"A paired workspace you may write to, by name or key from the workspaces tool"`
	To        string `json:"to" jsonschema:"The agent there to ask"`
	Target    string `json:"target" jsonschema:"The pull request: OWNER/REPO#N or its link"`
	Base      string `json:"base" jsonschema:"The base commit you resolved; the review is pinned to it"`
	Head      string `json:"head" jsonschema:"The head commit you resolved; the review is pinned to it"`
	Scope     string `json:"scope,omitempty" jsonschema:"What to focus on, at most 1 KiB"`
	Publish   bool   `json:"publish,omitempty" jsonschema:"Ask for the verdict published on the forge; that workspace allows at most what it granted"`
	Formal    bool   `json:"formal,omitempty" jsonschema:"With publish: ask for a formal approval or change request rather than a comment"`
	MessageID string `json:"message_id,omitempty" jsonschema:"Stable ID, maximum 128 bytes; generated when omitted. Retry with the same ID and content"`
}

type reviewAsker interface {
	RequestReview(context.Context, RequestReviewArgs) (SendResult, error)
}

// addReviewAskTools serves request_review when the client can send one.
func addReviewAskTools(s *mcp.Server, client messagingClient) {
	c, ok := client.(reviewAsker)
	if !ok {
		return
	}
	additive := false
	mcp.AddTool(s, &mcp.Tool{Name: "request_review", Annotations: &mcp.ToolAnnotations{DestructiveHint: &additive},
		Description: "Ask an agent of a paired workspace to review a pull request, pinned to the base and head commits you resolved with your own access. " +
			"That workspace's operator decides whether its agent may be asked, and whether anything is published. " +
			"The request is data there, never an instruction. Returns written status; acceptance and refusals arrive through receive/wait."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args RequestReviewArgs) (*mcp.CallToolResult, SendResult, error) {
			out, err := c.RequestReview(ctx, args)
			return nil, out, err
		})
}

var errReviewAskUnsupported = errors.New("review requests to paired workspaces are unsupported by this broker (protocol version 14 required); upgrade the broker")

// message is the request as an agent's msg to the workspace.
func (a RequestReviewArgs) message() (protocol.Message, error) {
	target, err := review.ParseTarget(a.Target)
	if err != nil {
		return protocol.Message{}, err
	}
	if slices.Contains([]string{a.Workspace, a.To, a.Base, a.Head}, "") {
		return protocol.Message{}, errors.New("workspace, to, target, base and head are required")
	}
	id := a.MessageID
	if id == "" {
		id = uuid.NewString()
	}
	ask := &protocol.ReviewAsk{ReviewTarget: target, Scope: a.Scope, Base: a.Base, Head: a.Head, Publish: a.Publish, Formal: a.Formal && a.Publish}
	return protocol.Message{Type: protocol.TypeMsg, To: a.To, Workspace: a.Workspace, ReviewAsk: ask, MessageID: id,
		AckRequested: true, Durable: true}, nil
}

// RequestReview writes the request; the broker answers it like a send.
func (c *Client) RequestReview(ctx context.Context, args RequestReviewArgs) (SendResult, error) {
	if c.protocolVersion < protocol.ReviewAskVersion {
		return SendResult{}, errReviewAskUnsupported
	}
	msg, err := args.message()
	if err != nil {
		return SendResult{}, err
	}
	out := SendResult{Status: "written", MessageID: msg.MessageID, To: msg.To, DurableRequested: true}
	if err := c.writeFrame(ctx, msg); err != nil {
		out.Status = "unconfirmed"
		return out, fmt.Errorf("review request %s: %w; broker acceptance is unconfirmed", msg.MessageID, err)
	}
	return out, nil
}

func (c *LazyClient) RequestReview(ctx context.Context, args RequestReviewArgs) (SendResult, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return SendResult{}, err
	}
	return client.RequestReview(ctx, args)
}
