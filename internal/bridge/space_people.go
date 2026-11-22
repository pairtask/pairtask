package bridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

// SpaceMembersArgs names the space to read; empty means the only one this
// workspace is in.
type SpaceMembersArgs struct {
	Space string `json:"space,omitempty" jsonschema:"the space ID; empty for the only space this workspace is in"`
}

// SpaceMembersResult is the people this workspace may see in a space.
type SpaceMembersResult struct {
	People      []protocol.SpacePerson `json:"people"`
	StatusKnown bool                   `json:"status_known"`
}

type spacePeopleClient interface {
	SpaceMembers(context.Context, string) (SpaceMembersResult, error)
}

// addSpaceTools serves space_members when the client can answer it.
func addSpaceTools(s *mcp.Server, client messagingClient) {
	c, ok := client.(spacePeopleClient)
	if !ok {
		return
	}
	mcp.AddTool(s, &mcp.Tool{Name: "space_members", Description: "Read who is in this workspace's Pairbook space: the people and workspaces it may see, " +
		"whether each is online, and the agents each lists there. Read-only. A listed agent is advertised, not reachable by that alone: " +
		"messaging still needs a paired workspace that shares it (see workspaces). status_known is false when the Pairbook keeps no status."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in SpaceMembersArgs) (*mcp.CallToolResult, SpaceMembersResult, error) {
			out, err := c.SpaceMembers(ctx, in.Space)
			return nil, out, err
		})
}

var errSpacePeopleUnsupported = errors.New("space_members needs a newer broker (protocol version 33); upgrade the broker")

// SpaceMembers asks the broker who this workspace may see in a space.
func (c *Client) SpaceMembers(ctx context.Context, space string) (SpaceMembersResult, error) {
	if c.protocolVersion < protocol.SpacePeopleVersion {
		return SpaceMembersResult{}, errSpacePeopleUnsupported
	}
	msg := protocol.Message{Type: protocol.TypeSpacePeopleGet, RequestID: uuid.NewString(), SpaceRequest: &protocol.SpaceRequest{Space: space}}
	reply, err := c.expect(msg.RequestID)
	if err != nil {
		return SpaceMembersResult{}, err
	}
	defer c.forget(msg.RequestID)
	if err := c.writeFrame(ctx, msg); err != nil {
		return SpaceMembersResult{}, err
	}
	out, err := c.awaitStatus(ctx, reply)
	if err != nil {
		return SpaceMembersResult{}, err
	}
	if out.Type == protocol.TypeError {
		return SpaceMembersResult{}, fmt.Errorf("%s: %s", out.Code, out.Detail)
	}
	return membersResult(out.SpaceResult), nil
}

func membersResult(r *protocol.SpaceResult) SpaceMembersResult {
	if r == nil {
		return SpaceMembersResult{People: []protocol.SpacePerson{}}
	}
	people := r.People
	if people == nil {
		people = []protocol.SpacePerson{}
	}
	return SpaceMembersResult{People: people, StatusKnown: r.StatusKnown}
}

func (c *LazyClient) SpaceMembers(ctx context.Context, space string) (SpaceMembersResult, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return SpaceMembersResult{}, err
	}
	return client.SpaceMembers(ctx, space)
}
