package desktop

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

type UnpairArgs struct {
	Contact   string `json:"contact"`
	Confirm   string `json:"confirm"`
	Epoch     int64  `json:"epoch"`
	Operation string `json:"operation"`
}

type LeaveArgs struct {
	Space   string `json:"space"`
	Confirm string `json:"confirm"`
}

func addDisconnectTools(s *mcp.Server, n *Node) {
	yes := true
	destructive := &mcp.ToolAnnotations{DestructiveHint: &yes, OpenWorldHint: &yes}
	mcp.AddTool(s, &mcp.Tool{Name: "pair_unpair", Description: "End the exact pairing confirmed by the operator. Keep its conversation history. Use the displayed full key and pairing epoch; reuse the operation ID for a retry.", Annotations: destructive},
		func(ctx context.Context, _ *mcp.CallToolRequest, args UnpairArgs) (*mcp.CallToolResult, *protocol.ContactUnpairView, error) {
			if args.Operation == "" {
				return nil, nil, errors.New("an operation ID is required")
			}
			out, err := n.ask(ctx, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpUnpair, ContactKey: args.Contact, ContactConfirm: args.Confirm, ContactEpoch: args.Epoch, MessageID: args.Operation}, protocol.TypeOperatorContactResult)
			if err != nil {
				return nil, nil, err
			}
			return nil, out.ContactUnpaired, missing(out.ContactUnpaired == nil)
		})
	mcp.AddTool(s, &mcp.Tool{Name: "pairbook_leave", Description: "Leave the exact Pairbook space confirmed by the operator. Revokes this workspace's membership before removing its saved space.", Annotations: destructive},
		func(ctx context.Context, _ *mcp.CallToolRequest, args LeaveArgs) (*mcp.CallToolResult, struct{}, error) {
			return nil, struct{}{}, n.leaveSpace(ctx, args)
		})
}
