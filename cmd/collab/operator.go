package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"

	"github.com/google/uuid"
)

// The operator endpoint exists only in secured and serves only the operator UID.
const defaultOperatorSocket = "/run/collab-ai-operator/operator.sock"

const operatorUsage = "usage: pairtask operator send [--socket PATH] (--to ID | --group ID,ID | --all [--room ROOM]) [--message-id ID] [--in-reply-to ID] [--json] [--timeout 5s] TEXT\n       pairtask operator receive [--socket PATH] [--after SEQ] [--upper SEQ] [--limit N] [--json]\n       pairtask operator ack [--socket PATH] [--json] MESSAGE_ID\n       pairtask operator history [--socket PATH] [--before SEQ] [--upper SEQ] [--limit N] [--json]\n       pairtask operator chat [--socket PATH] [--timeout 5s]\n       pairtask operator inbox status [--socket PATH] [--json]\n       pairtask operator inbox ack [--socket PATH] [--from AGENT] [--after SEQ] [--upper SEQ] [--limit N] [--all] [--json]\n       pairtask operator room (list | create ROOM | add ROOM AGENT | remove ROOM AGENT) [--socket PATH] [--json]"

type operatorSend struct {
	socket, to, group, messageID string
	inReplyTo, room              string
	all, asJSON                  bool
	timeout                      time.Duration
	text                         string
}

type operatorCommand func(ctx context.Context, args []string, stdout, stderr io.Writer) int

var operatorCommands = map[string]operatorCommand{
	"send":    runOperatorSend,
	"receive": runOperatorReceive,
	"ack":     runOperatorAck,
	"history": runOperatorHistory,
	"chat":    runOperatorChat,
	"inbox":   runOperatorInbox,
	"room":    runOperatorRoom,
}

func runOperator(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, operatorUsage)
		return 2
	}
	run, ok := operatorCommands[args[0]]
	if !ok {
		fmt.Fprintln(stderr, operatorUsage)
		return 2
	}
	return run(ctx, args[1:], stdout, stderr)
}

func runOperatorSend(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, code := parseOperatorSend(args, stderr)
	if code != -1 {
		return code
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	resp, err := sendOperator(ctx, cfg.socket, cfg.request())
	if err != nil {
		fmt.Fprintln(stderr, "operator send failed:", err)
		return 1
	}
	return reportOperator(resp, cfg.asJSON, stdout, stderr)
}

// reportOperator prints the broker response; JSON output includes rejections.
func reportOperator(resp protocol.Message, asJSON bool, stdout, stderr io.Writer) int {
	accepted := resp.Type == protocol.TypeAck && resp.Stage == protocol.StageAccepted
	if asJSON {
		if err := json.NewEncoder(stdout).Encode(resp); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else if accepted {
		writeOperatorAccepted(stdout, resp)
	}
	if !accepted {
		fmt.Fprintf(stderr, "operator send rejected: %q: %q\n", resp.Code, resp.Detail)
		return 1
	}
	return 0
}

func parseOperatorSend(args []string, stderr io.Writer) (operatorSend, int) {
	var cfg operatorSend
	flags := flag.NewFlagSet("operator send", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.socket, "socket", defaultOperatorSocket, "operator Unix socket path (secured only)")
	flags.StringVar(&cfg.to, "to", "", "one agent ID")
	flags.StringVar(&cfg.group, "group", "", "comma-separated agent IDs")
	flags.BoolVar(&cfg.all, "all", false, "all connected agents that can display origin")
	flags.StringVar(&cfg.room, "room", "", "with --all: only this room's members (default: the Square, everyone)")
	flags.StringVar(&cfg.messageID, "message-id", "", "stable message ID for retries (generated when omitted)")
	flags.StringVar(&cfg.inReplyTo, "in-reply-to", "", "message ID this answers (a correlation claim)")
	flags.BoolVar(&cfg.asJSON, "json", false, "emit the broker response as JSON")
	flags.DurationVar(&cfg.timeout, "timeout", 5*time.Second, "overall timeout, greater than zero and at most 30s")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, 0
		}
		return cfg, 2
	}
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if err := cfg.validate(set, flags.Args()); err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, operatorUsage)
		return cfg, 2
	}
	if cfg.messageID == "" {
		cfg.messageID = uuid.NewString()
	}
	return cfg, -1
}

func (cfg *operatorSend) validate(set map[string]bool, positional []string) error {
	if err := cfg.validateScope(set); err != nil {
		return err
	}
	if len(positional) != 1 || positional[0] == "" {
		return errors.New("exactly one non-empty TEXT argument is required")
	}
	if err := cfg.validateOptions(); err != nil {
		return err
	}
	cfg.text = positional[0]
	return nil
}

var errScope = errors.New("choose exactly one of --to, --group or --all")

// Scope flags count when given, even with an empty value, so an empty --group
// can't silently fall back to another scope.
func (cfg operatorSend) validateScope(set map[string]bool) error {
	checks := map[string]func() error{"to": cfg.checkTo, "group": cfg.checkGroup, "all": cfg.checkAll}
	var chosen []string
	for name := range checks {
		if set[name] {
			chosen = append(chosen, name)
		}
	}
	if len(chosen) != 1 {
		return errScope
	}
	return checks[chosen[0]]()
}

func (cfg operatorSend) checkTo() error {
	if cfg.to == "" {
		return errors.New("--to must not be empty")
	}
	return nil
}

func (cfg operatorSend) checkGroup() error {
	if slices.Contains(strings.Split(cfg.group, ","), "") {
		return errors.New("--group must not contain empty agent IDs")
	}
	return nil
}

func (cfg operatorSend) checkAll() error {
	if !cfg.all {
		return errScope
	}
	return nil
}

func (cfg operatorSend) validateOptions() error {
	if cfg.socket == "" {
		return errors.New("a socket path is required")
	}
	if cfg.room != "" && !cfg.all {
		return errRoomScope
	}
	if max(len(cfg.messageID), len(cfg.inReplyTo)) > 128 {
		return errors.New("--message-id and --in-reply-to must be at most 128 bytes")
	}
	if cfg.timeout <= 0 || cfg.timeout > 30*time.Second {
		return errors.New("timeout must be greater than zero and at most 30s")
	}
	return nil
}

func (cfg operatorSend) request() protocol.Message {
	payload, _ := json.Marshal(map[string]string{"text": cfg.text})
	msg := protocol.Message{Type: protocol.TypeOperatorSend, MessageID: cfg.messageID, InReplyTo: cfg.inReplyTo, Payload: payload}
	switch {
	case cfg.all:
		msg.To, msg.Room = protocol.Broadcast, cfg.room
	case cfg.group != "":
		msg.Group = strings.Split(cfg.group, ",")
	default:
		msg.To = cfg.to
	}
	return msg
}

func sendOperator(ctx context.Context, socket string, req protocol.Message) (protocol.Message, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return protocol.Message{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if err := protocol.WriteFrame(conn, req); err != nil {
		return protocol.Message{}, err
	}
	var resp protocol.Message
	if err := protocol.ReadFrame(bufio.NewReader(conn), &resp); err != nil {
		return protocol.Message{}, err
	}
	return resp, nil
}

func writeOperatorAccepted(w io.Writer, resp protocol.Message) {
	// Agent and message IDs are peer-controlled; quote them so terminal controls stay inert.
	ids := make([]string, 0, len(resp.Recipients))
	for _, r := range resp.Recipients {
		ids = append(ids, fmt.Sprintf("%q", r.AgentID))
	}
	state := "accepted"
	if resp.Repeated {
		state = "already accepted (retry)"
	}
	fmt.Fprintf(w, "%q %s seq=%d recipients=%s\n", resp.MessageID, state, resp.Seq, strings.Join(ids, ","))
	if len(resp.Excluded) != 0 {
		excluded := make([]string, 0, len(resp.Excluded))
		for _, id := range resp.Excluded {
			excluded = append(excluded, fmt.Sprintf("%q", id))
		}
		fmt.Fprintf(w, "excluded (cannot display origin or inbox full): %s\n", strings.Join(excluded, ","))
	}
}
