package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorRoomUsage = "usage: pairtask operator room (list | create ROOM | add ROOM AGENT | remove ROOM AGENT) [--socket PATH] [--json] [--timeout 5s]"

// roomVerbs are the room operations and their positional arguments.
var roomVerbs = map[string]struct {
	op   string
	args int
}{
	"list":   {protocol.RoomOpList, 0},
	"create": {protocol.RoomOpCreate, 1},
	"add":    {protocol.RoomOpAdd, 2},
	"remove": {protocol.RoomOpRemove, 2},
}

// runOperatorRoom lists rooms or changes one; the answer is every room.
func runOperatorRoom(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, req, code := parseOperatorRoom(args, stderr)
	if code != -1 {
		return code
	}
	resp, err := call.expect(ctx, req, expectation{protocol.TypeOperatorRoomResult, "operator room"}, stderr)
	if err != nil {
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, resp)
	}
	writeRooms(stdout, resp.RoomList)
	return 0
}

func parseOperatorRoom(args []string, stderr io.Writer) (operatorCall, protocol.Message, int) {
	var call operatorCall
	if len(args) == 0 {
		fmt.Fprintln(stderr, operatorRoomUsage)
		return call, protocol.Message{}, 2
	}
	verb, ok := roomVerbs[args[0]]
	if !ok {
		fmt.Fprintln(stderr, operatorRoomUsage)
		return call, protocol.Message{}, 2
	}
	req := protocol.Message{Type: protocol.TypeOperatorRoom, MessageID: uuid.NewString(), RoomOp: verb.op}
	flags := flag.NewFlagSet("operator room", flag.ContinueOnError)
	call.register(flags)
	code := parseFlags(flags, args[1:], operatorRoomUsage, stderr, func(rest []string) error {
		if len(rest) != verb.args {
			return fmt.Errorf("room %s takes %d arguments", args[0], verb.args)
		}
		if err := roomRequest(&req, rest); err != nil {
			return err
		}
		return call.validate()
	})
	return call, req, code
}

// roomRequest fills the room and agent from the positional arguments.
func roomRequest(req *protocol.Message, rest []string) error {
	if len(rest) == 0 {
		return nil
	}
	if err := protocol.ValidateRoomID(rest[0]); err != nil {
		return err
	}
	req.Room = rest[0]
	if len(rest) == 2 {
		req.AgentID = rest[1]
		return protocol.ValidateAgentID(rest[1])
	}
	return nil
}

func writeRooms(w io.Writer, list *protocol.RoomList) {
	if list == nil || len(list.Rooms) == 0 {
		fmt.Fprintln(w, "No rooms. Agents talk in the Square (everyone).")
		return
	}
	for _, r := range list.Rooms {
		members := "(empty)"
		if len(r.Members) > 0 {
			members = fmt.Sprintf("%q", strings.Join(r.Members, ", "))
		}
		if r.MembersTruncated {
			members += fmt.Sprintf(" (+%d more)", r.MembersTotal-len(r.Members))
		}
		fmt.Fprintf(w, "#%s  %s\n", r.ID, members)
	}
	if list.Truncated {
		fmt.Fprintf(w, "(showing %d of %d rooms)\n", len(list.Rooms), list.Total)
	}
}

var errRoomScope = errors.New("--room applies to --all only")
