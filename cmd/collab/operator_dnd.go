package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// pairtask operator dnd (#422): turn one agent's do-not-disturb on or off,
// or show it. While it is on the agent still gets every message, kept
// without interrupting it; `operator send --urgent` interrupts anyway.

const operatorDNDUsage = "usage: pairtask operator dnd --agent ID [on | off] [--socket PATH] [--json] [--timeout 5s]\n       without on or off it shows the setting and changes nothing"

func runOperatorDND(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, req, code := parseOperatorDND(args, stderr)
	if code != -1 {
		return code
	}
	resp, err := call.expect(ctx, req, expectation{protocol.TypeOperatorAttentionResult, "operator dnd"}, stderr)
	if err != nil {
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, resp)
	}
	if resp.AttentionState == nil {
		fmt.Fprintln(stderr, "operator dnd: the broker's answer has no setting")
		return 1
	}
	fmt.Fprintln(stdout, dndLine(*resp.AttentionState))
	return 0
}

func parseOperatorDND(args []string, stderr io.Writer) (operatorCall, protocol.Message, int) {
	var call operatorCall
	req := protocol.Message{Type: protocol.TypeOperatorAttention, MessageID: uuid.NewString(), AttentionOp: protocol.AttentionStatus}
	flags := flag.NewFlagSet("operator dnd", flag.ContinueOnError)
	call.register(flags)
	flags.StringVar(&req.AgentID, "agent", "", "the local agent")
	code := parseFlags(flags, args, operatorDNDUsage, stderr, func(rest []string) error {
		if err := protocol.ValidateAgentID(req.AgentID); err != nil {
			return errors.New("--agent names one local agent: " + err.Error())
		}
		if err := dndAction(rest, &req); err != nil {
			return err
		}
		return call.validate()
	})
	return call, req, code
}

// dndAction reads the optional on or off; none keeps the request a read.
func dndAction(rest []string, req *protocol.Message) error {
	switch {
	case len(rest) == 0:
		return nil
	case len(rest) == 1 && (rest[0] == protocol.AttentionOn || rest[0] == protocol.AttentionOff):
		req.AttentionOp = rest[0]
		return nil
	}
	return errors.New("the action is on or off, or nothing to show the setting")
}

// dndLine is the setting for a person: on or off, whether it is enforced
// now, and what is waiting.
func dndLine(s protocol.AttentionState) string {
	line := s.AgentID + ": do-not-disturb off"
	if s.DND {
		line = s.AgentID + ": do-not-disturb on" + dndEnforcement(s)
	}
	return fmt.Sprintf("%s; %d quiet and unacknowledged; inbox %d of %d", line, s.Pending, s.InboxUsed, s.InboxLimit)
}

// dndEnforcement says why a setting that is on isn't keeping the agent
// quiet now, or nothing when it is.
func dndEnforcement(s protocol.AttentionState) string {
	switch s.Reason {
	case "":
		return ""
	case protocol.AttentionReasonOffline:
		return " (the agent isn't connected; it applies when it connects)"
	case protocol.AttentionReasonOldAdapter:
		return " (NOT enforced: the agent's adapter predates do-not-disturb, so messages still interrupt it; restart the agent after upgrading)"
	}
	return " (not enforced: " + s.Reason + ")"
}
