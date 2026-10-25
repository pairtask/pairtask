package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"

	"github.com/pairtask/pairtask/internal/protocol"
)

const joinUsage = "usage: pairtask join --id ID [--socket PATH] [--room ROOM]... [--dry-run] -- COMMAND [ARG]..."

// joinOptions is what `pairtask join` was asked for.
type joinOptions struct {
	ID, Socket string
	Rooms      protocol.RoomFlags
	DryRun     bool
	Command    []string
}

// runJoin starts any harness with the pairtask adapter environment (#155):
// PAIRTASK_AGENT_ID, PAIRTASK_SOCKET, PAIRTASK_ROOMS (a JSON array) and
// PAIRTASK_MCP. It joins nothing by itself: the harness integrates only if it
// runs the bridge with that environment, for tools and pushed messages.
func runJoin(args []string, rt startRuntime) int {
	opts, err := parseJoin(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(rt.Stdout, joinUsage)
		return 0
	}
	if err != nil {
		fmt.Fprintln(rt.Stderr, err)
		fmt.Fprintln(rt.Stderr, joinUsage)
		return 2
	}
	path, err := exec.LookPath(opts.Command[0])
	if err != nil {
		fmt.Fprintln(rt.Stderr, err)
		return 127
	}
	plan := startPlan{Argv: append([]string{path}, opts.Command[1:]...), Env: agentEnv(opts.ID, opts.Socket, opts.Rooms, rt.Layout.MCPBinary)}
	if opts.DryRun {
		printPlan(plan, rt.Stdout)
		return 0
	}
	return launch(plan, rt)
}

func parseJoin(args []string) (joinOptions, error) {
	var opts joinOptions
	ours, command := splitNative(args)
	flags := flag.NewFlagSet("join", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.ID, "id", "", "logical agent ID (required)")
	flags.StringVar(&opts.Socket, "socket", "", "broker Unix socket path")
	flags.Var(&opts.Rooms, "room", "join this room on registration (repeatable)")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "print the command and environment; run nothing")
	if err := flags.Parse(ours); err != nil {
		return opts, err
	}
	if flags.NArg() > 0 {
		return opts, fmt.Errorf("unexpected argument %q; the command goes after --", flags.Arg(0))
	}
	opts.Command = command
	return opts, opts.validate()
}

func (o joinOptions) validate() error {
	if len(o.Command) == 0 || o.Command[0] == "" {
		return errors.New("a command to run is required after --")
	}
	return protocol.ValidateAgentID(o.ID)
}
