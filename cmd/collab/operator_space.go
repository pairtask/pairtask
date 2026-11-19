package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorSpaceUsage = `usage: pairtask space COMMAND [--socket PATH] [--json] [--timeout 30s]
  list
  create --directory URL (or --pairbook URL) [--name NAME] [--id ID]
  invite   [--space ID] [--valid 24h]
  join LINK [--name NAME]
  requests [--space ID]
  approve FP [--space ID] [--valid 2160h]
  members  [--space ID]
  publish HOST:PORT... [--space ID] [--valid 24h]
  revoke FP [--space ID]
  leave    [--space ID]
  delete   [--space ID]
  connect FP [--space ID]
--space may be left out when exactly one space is pinned.`

// spaceCommand is how one space command's words map onto its request:
// the op and what its positional arguments are.
type spaceCommand struct {
	op   string
	args func(r *protocol.SpaceRequest, words []string) error
}

var spaceCommands = map[string]spaceCommand{
	"list":     {protocol.SpaceOpList, noWords},
	"create":   {protocol.SpaceOpCreate, needDirectory},
	"invite":   {protocol.SpaceOpInvite, noWords},
	"join":     {protocol.SpaceOpJoin, oneWord(func(r *protocol.SpaceRequest, w string) { r.Link = w })},
	"requests": {protocol.SpaceOpRequests, noWords},
	"approve":  {protocol.SpaceOpApprove, oneWord(func(r *protocol.SpaceRequest, w string) { r.Node = w })},
	"members":  {protocol.SpaceOpMembers, noWords},
	"publish":  {protocol.SpaceOpPublish, addresses},
	"revoke":   {protocol.SpaceOpRevoke, oneWord(func(r *protocol.SpaceRequest, w string) { r.Node = w })},
	"leave":    {protocol.SpaceOpLeave, noWords},
	"delete":   {protocol.SpaceOpDelete, noWords},
	"connect":  {protocol.SpaceOpConnect, oneWord(func(r *protocol.SpaceRequest, w string) { r.Node = w })},
}

func noWords(_ *protocol.SpaceRequest, words []string) error {
	if len(words) != 0 {
		return fmt.Errorf("unexpected argument %q", words[0])
	}
	return nil
}

func needDirectory(r *protocol.SpaceRequest, words []string) error {
	if r.Directory == "" {
		return errors.New("create needs --directory URL")
	}
	return noWords(r, words)
}

func oneWord(set func(*protocol.SpaceRequest, string)) func(*protocol.SpaceRequest, []string) error {
	return func(r *protocol.SpaceRequest, words []string) error {
		if len(words) != 1 {
			return errors.New("expected exactly one argument")
		}
		set(r, words[0])
		return nil
	}
}

func addresses(r *protocol.SpaceRequest, words []string) error {
	if len(words) == 0 {
		return errors.New("publish needs at least one HOST:PORT")
	}
	r.Addresses = words
	return nil
}

// runOperatorSpace runs a Pairbook space command (pairtask space, or
// pairtask operator space): one operator_space request to the broker.
func runOperatorSpace(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, req, code := parseSpace(args, stderr)
	if code != -1 {
		return code
	}
	resp, err := call.expect(ctx, protocol.Message{Type: protocol.TypeOperatorSpace, MessageID: uuid.NewString(), SpaceRequest: &req},
		expectation{protocol.TypeOperatorSpaceResult, "space " + req.Op}, stderr)
	if err != nil {
		return 1
	}
	result := resp.SpaceResult
	if result == nil {
		result = &protocol.SpaceResult{}
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, result)
	}
	printSpace(stdout, req.Op, result)
	return 0
}

// parseSpace reads COMMAND, its words and flags, in either order.
func parseSpace(args []string, stderr io.Writer) (operatorCall, protocol.SpaceRequest, int) {
	var call operatorCall
	var req protocol.SpaceRequest
	if len(args) == 0 {
		fmt.Fprintln(stderr, operatorSpaceUsage)
		return call, req, 2
	}
	cmd, ok := spaceCommands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "unknown space command %q\n%s\n", args[0], operatorSpaceUsage)
		return call, req, 2
	}
	req.Op = cmd.op
	flags := spaceFlags(&call, &req)
	words, rest := leadingWords(args[1:])
	code := parseFlags(flags, rest, operatorSpaceUsage, stderr, func(more []string) error {
		if err := call.validate(); err != nil {
			return err
		}
		return cmd.args(&req, append(words, more...))
	})
	return call, req, code
}

// spaceFlags are every space command's flags; valid sets req.ValidFor.
func spaceFlags(call *operatorCall, req *protocol.SpaceRequest) *flag.FlagSet {
	flags := flag.NewFlagSet("space", flag.ContinueOnError)
	call.register(flags)
	_ = flags.Set("timeout", "30s") // directory calls may take the broker's whole attempt budget
	flags.StringVar(&req.Space, "space", "", "space ID; may be left out when exactly one is pinned")
	flags.StringVar(&req.Directory, "directory", "", "create: the Pairbook directory, https://HOST[:PORT]")
	flags.StringVar(&req.Directory, "pairbook", "", "create: same as --directory")
	flags.StringVar(&req.Name, "name", "", "create, join: this workspace's name in the space")
	flags.StringVar(&req.ID, "id", "", "create: the space ID; random when empty")
	flags.Func("valid", "invite, approve, publish: how long it stays valid", func(v string) error {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Millisecond {
			return errors.New("--valid must be a duration of at least 1ms")
		}
		req.ValidFor = d.Milliseconds()
		return nil
	})
	return flags
}

// leadingWords splits off the arguments before the first flag.
func leadingWords(args []string) ([]string, []string) {
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			return args[:i], args[i:]
		}
	}
	return args, nil
}

func printSpace(w io.Writer, op string, r *protocol.SpaceResult) {
	for _, p := range r.Pins {
		owned := ""
		if p.Owned {
			owned = "  (owner)"
		}
		fmt.Fprintf(w, "%s  owner %s  directory %s%s\n", p.Space, p.Owner, p.Directory, owned)
	}
	if r.Invite != "" {
		fmt.Fprintln(w, r.Invite)
		fmt.Fprintln(w, "Share this link privately: it lets one workspace ask to join, until it expires.")
	}
	printSpaceMembers(w, r.Requests, "requested")
	printSpaceMembers(w, r.Members, "until")
	if op != protocol.SpaceOpList && emptySpaceResult(r) {
		fmt.Fprintln(w, "done")
	}
}

// emptySpaceResult: r carries nothing to show, as after approve or leave.
func emptySpaceResult(r *protocol.SpaceResult) bool {
	return len(r.Pins)+len(r.Requests)+len(r.Members) == 0 && r.Invite == ""
}

func printSpaceMembers(w io.Writer, members []protocol.SpaceMember, at string) {
	for _, m := range members {
		role := ""
		if m.Role != "" {
			role = "  " + m.Role
		}
		fmt.Fprintf(w, "%s  %q%s  %s %s\n", m.Node, m.Name, role, at, m.At.UTC().Format(time.RFC3339))
	}
}
