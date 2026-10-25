package bridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/protocol"
)

// RoomsResult is the agent's rooms after a room call.
type RoomsResult struct {
	Rooms []string `json:"rooms"`
}

type roomClient interface {
	Room(context.Context, protocol.Message) (RoomsResult, error)
}

const roomsMeaning = " Rooms are opt-in spaces for a group's discussion: send to \"*\" with room R to reach R's members and the operator; \"*\" without a room is the Square, everyone. Direct messages work with anyone."

func addRoomTools(s *mcp.Server, c roomClient) {
	type roomArgs struct {
		Room string `json:"room" jsonschema:"Room name: 1 to 64 of a-z, 0-9 and -"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "join_room", Description: "Join a room, creating it if it doesn't exist; your other rooms stay." + roomsMeaning},
		func(ctx context.Context, _ *mcp.CallToolRequest, args roomArgs) (*mcp.CallToolResult, RoomsResult, error) {
			out, err := c.Room(ctx, protocol.Message{Type: protocol.TypeRoomJoin, Room: args.Room})
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "leave_room", Description: "Leave a room; messages already delivered to you stay." + roomsMeaning},
		func(ctx context.Context, _ *mcp.CallToolRequest, args roomArgs) (*mcp.CallToolResult, RoomsResult, error) {
			out, err := c.Room(ctx, protocol.Message{Type: protocol.TypeRoomLeave, Room: args.Room})
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_rooms", Description: "List the rooms you are in, as the broker has them now." + roomsMeaning},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, RoomsResult, error) {
			out, err := c.Room(ctx, protocol.Message{Type: protocol.TypeRoomList})
			return nil, out, err
		})
}

var errRoomsUnsupported = errors.New("rooms are unsupported by this broker (protocol version 9 required); upgrade the broker")

// Room sends one room frame and waits for its correlated answer.
func (c *Client) Room(ctx context.Context, msg protocol.Message) (RoomsResult, error) {
	if c.protocolVersion < protocol.RoomVersion {
		return RoomsResult{}, errRoomsUnsupported
	}
	msg.RequestID = uuid.NewString()
	reply, err := c.expect(msg.RequestID)
	if err != nil {
		return RoomsResult{}, err
	}
	defer c.forget(msg.RequestID)
	if err := c.writeFrame(ctx, msg); err != nil {
		return RoomsResult{}, err
	}
	out, err := c.awaitStatus(ctx, reply)
	if err != nil {
		return RoomsResult{}, err
	}
	if out.Type == protocol.TypeError {
		return RoomsResult{}, fmt.Errorf("%s: %s", out.Code, out.Detail)
	}
	rooms := out.Rooms
	if rooms == nil {
		rooms = []string{}
	}
	return RoomsResult{Rooms: rooms}, nil
}

func (c *LazyClient) Room(ctx context.Context, msg protocol.Message) (RoomsResult, error) {
	client, err := c.connection(ctx)
	if err != nil {
		return RoomsResult{}, err
	}
	return client.Room(ctx, msg)
}

// checkRoom refuses a room on anything but "*", or against a broker without
// rooms, rather than letting the broker ignore it and post to everyone.
func (c *Client) checkRoom(request SendRequest) error {
	if protocol.RoomScope(request.Room) == "" {
		return nil
	}
	if request.To != protocol.Broadcast {
		return errors.New("room applies to \"*\" only")
	}
	if c.protocolVersion < protocol.RoomVersion {
		return errRoomsUnsupported
	}
	return nil
}
