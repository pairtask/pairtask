package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/forge/github"
	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorChatUsage = "usage: pairtask operator chat [--socket PATH] [--timeout 5s]"

func runOperatorChat(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	socket, timeout, code := parseChat(args, stderr)
	if code != -1 {
		return code
	}
	output := dashboardTerminal(stdout)
	if output == nil {
		fmt.Fprintln(stderr, "pairtask operator chat requires an interactive terminal; use pairtask operator send, receive, ack or history instead")
		return 2
	}
	opts := chat.Options{Dial: chat.SocketDialer(socket, timeout), Bounds: chat.Bounds{ResolveChange: changeLookup(forge.Registry{
		"github": github.New(github.Production()),
	})}}
	m, err := chat.Run(ctx, opts, os.Stdin, output)
	if err != nil {
		fmt.Fprintln(stderr, "operator chat:", err)
		return 1
	}
	return reportChatExit(m.State(), stderr)
}

// changeLookup looks a change up with this operator's own code host
// access, for a review asked of a paired workspace's agent (#297).
func changeLookup(forges forge.Registry) chat.ResolveFunc {
	return func(ctx context.Context, t protocol.ReviewTarget) (protocol.Revisions, error) {
		f, ok := forges[t.Forge]
		if !ok {
			return protocol.Revisions{}, forge.Errorf(forge.ClassUnsupported, "no %s adapter here", t.Forge)
		}
		return f.Resolve(ctx, t)
	}
}

// reportChatExit explains a failed start, or a send whose outcome is unknown.
func reportChatExit(state chat.StateView, stderr io.Writer) int {
	if state.Fatal != "" {
		fmt.Fprintln(stderr, "operator chat:", state.Fatal)
	}
	if in := state.Intent; unresolved(in) {
		fmt.Fprintf(stderr, "send %q: outcome unknown; this chat can't resolve it after exit. pairtask operator history shows it if the broker accepted it.\n", in.MessageID)
	}
	return state.ExitCode
}

func unresolved(in *chat.IntentView) bool {
	if in == nil {
		return false
	}
	switch in.State {
	case chat.IntentSending, chat.IntentUnknown:
		return true
	}
	return false
}

func parseChat(args []string, stderr io.Writer) (string, time.Duration, int) {
	var socket string
	var timeout time.Duration
	flags := flag.NewFlagSet("operator chat", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&socket, "socket", defaultOperatorSocket, "operator Unix socket path (secured only)")
	flags.DurationVar(&timeout, "timeout", 5*time.Second, "connect and open timeout, greater than zero and at most 30s")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", 0, 0
		}
		return "", 0, 2
	}
	if err := validateChat(socket, timeout, flags.NArg()); err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, operatorChatUsage)
		return "", 0, 2
	}
	return socket, timeout, -1
}

func validateChat(socket string, timeout time.Duration, positional int) error {
	if positional != 0 {
		return errors.New("chat takes no positional arguments")
	}
	if socket == "" {
		return errors.New("a socket path is required")
	}
	if timeout <= 0 || timeout > 30*time.Second {
		return errors.New("timeout must be greater than zero and at most 30s")
	}
	return nil
}
