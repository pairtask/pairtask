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
	"github.com/pairtask/pairtask/internal/personkey"
	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorChatUsage = "usage: pairtask operator chat [--socket PATH] [--timeout 5s] [--unsigned]"

// chatConfig is the chat's flags.
type chatConfig struct {
	socket   string
	timeout  time.Duration
	unsigned bool
}

func runOperatorChat(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, code := parseChat(args, stderr)
	if code != -1 {
		return code
	}
	output := dashboardTerminal(stdout)
	if output == nil {
		fmt.Fprintln(stderr, "pairtask operator chat requires an interactive terminal; use pairtask operator send, receive, ack or history instead")
		return 2
	}
	forges := forge.Registry{"github": github.New(github.Production())}
	seal, err := chatSealer(ctx, cfg, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "operator chat:", err)
		return 1
	}
	opts := chat.Options{Dial: chat.SocketDialer(cfg.socket, cfg.timeout), Bounds: chat.Bounds{ResolveChange: changeLookup(forges), CheckPins: pinCheck(forges)},
		Seal: seal}
	m, err := chat.Run(ctx, opts, os.Stdin, output)
	if err != nil {
		fmt.Fprintln(stderr, "operator chat:", err)
		return 1
	}
	return reportChatExit(m.State(), stderr)
}

// chatSealer is the chat's sealer: this account's keys, registered with
// the broker. With no keys yet, or --unsigned, the chat sends unsigned and
// says so before it starts; with keys that can't sign, it doesn't start.
func chatSealer(ctx context.Context, cfg chatConfig, stderr io.Writer) (func(protocol.Message) string, error) {
	if cfg.unsigned {
		fmt.Fprintln(stderr, "operator chat: --unsigned: your messages carry no signature")
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	seal, err := operatorSealer(ctx, cfg.socket)
	switch {
	case errors.Is(err, personkey.ErrNoKeys):
		fmt.Fprintln(stderr, "operator chat: your messages go unsigned until you run `pairtask operator identity init`")
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("can't sign: %w%s", err, errUnsignedHint)
	}
	return seal, nil
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

// pinCheck checks explicit pins with this operator's own code host
// access, after the request is sent (#304); advice only.
func pinCheck(forges forge.Registry) chat.PinCheckFunc {
	return func(ctx context.Context, t protocol.ReviewTarget, pins protocol.Revisions) []protocol.ReviewAdvisory {
		f, ok := forges[t.Forge]
		if !ok {
			return []protocol.ReviewAdvisory{{Code: protocol.AdvisoryNotChecked, Class: forge.ClassUnsupported}}
		}
		var out []protocol.ReviewAdvisory
		for _, c := range forge.CheckPins(ctx, f, t, pins) {
			out = append(out, protocol.ReviewAdvisory{Code: c.Code, Current: c.Current, Class: c.Class})
		}
		return out
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

func parseChat(args []string, stderr io.Writer) (chatConfig, int) {
	var cfg chatConfig
	flags := flag.NewFlagSet("operator chat", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.socket, "socket", defaultOperatorSocket, "operator Unix socket path (secured only)")
	flags.DurationVar(&cfg.timeout, "timeout", 5*time.Second, "connect and open timeout, greater than zero and at most 30s")
	flags.BoolVar(&cfg.unsigned, "unsigned", false, "send without your signature even though you have keys")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, 0
		}
		return cfg, 2
	}
	if err := validateChat(cfg.socket, cfg.timeout, flags.NArg()); err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, operatorChatUsage)
		return cfg, 2
	}
	return cfg, -1
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
