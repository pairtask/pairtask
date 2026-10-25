// Command pairtask (also pk, and the deprecated collab) provides local operator
// tools for collaboration and budgets.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pairtask/pairtask/internal/status"
)

func main() {
	if filepath.Base(os.Args[0]) == "collab" {
		fmt.Fprintln(os.Stderr, "collab is deprecated; use pairtask (or pk) instead")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runStatus(ctx, args, stdout, stderr)
	}
	switch args[0] {
	case "budget":
		return runBudget(args[1:], stdout, stderr)
	case "dashboard":
		return runDashboard(ctx, args[1:], stdout, stderr)
	case "operator":
		return runOperator(ctx, args[1:], stdout, stderr)
	case "start":
		return runStart(args[1:], startRuntime{Stdout: stdout, Stderr: stderr, Layout: defaultStartLayout(), Exec: syscall.Exec,
			Status: status.Fetch, Chdir: os.Chdir})
	case "join":
		return runJoin(args[1:], startRuntime{Stdout: stdout, Stderr: stderr, Layout: defaultStartLayout(), Exec: syscall.Exec})
	}
	return runStatus(ctx, args, stdout, stderr)
}

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, code := parseOptions(args, stderr)
	if code != -1 {
		return code
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	out, queryErr := status.Fetch(ctx, cfg.socket)
	var writeErr error
	if cfg.asJSON {
		writeErr = json.NewEncoder(stdout).Encode(out)
	} else {
		writeErr = status.WriteText(stdout, out)
	}
	if writeErr != nil {
		fmt.Fprintln(stderr, writeErr)
		return 1
	}
	if queryErr != nil {
		return 1
	}
	return 0
}

type options struct {
	socket  string
	asJSON  bool
	timeout time.Duration
}

// A -1 code means parsing succeeded and execution should continue.
func parseOptions(args []string, stderr io.Writer) (options, int) {
	var cfg options
	if len(args) == 0 || args[0] != "status" {
		fmt.Fprintln(stderr, "usage: pairtask status [--socket PATH] [--json] [--timeout 3s]\n       pairtask dashboard [--socket PATH] [--interval 2s] [--timeout 3s]\n       pairtask budget create NAME --tokens N [--json]\n       pairtask budget status NAME [--socket PATH] [--json]\n       pairtask operator send [--socket PATH] (--to ID | --group ID,ID | --all [--room ROOM]) [--message-id ID] [--in-reply-to ID] [--json] TEXT\n       pairtask operator receive [--socket PATH] [--after SEQ] [--upper SEQ] [--limit N] [--json]\n       pairtask operator ack [--socket PATH] [--json] MESSAGE_ID\n       pairtask operator room (list | create ROOM | add ROOM AGENT | remove ROOM AGENT) [--socket PATH] [--json]\n       pairtask start (claude|codex|pi) [--id ID] [--model NAME] [--effort LEVEL] [--socket PATH] [--room ROOM]... [--resume] [--dry-run] [-- agent arguments]\n       pairtask join --id ID [--socket PATH] [--room ROOM]... [--dry-run] -- COMMAND [ARG]...")
		return cfg, 2
	}
	socketDefault, ok := defaultSocket(stderr)
	if !ok {
		return cfg, 2
	}
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.socket, "socket", socketDefault, "broker Unix socket path")
	flags.BoolVar(&cfg.asJSON, "json", false, "emit versioned JSON, including unavailable/error results")
	flags.DurationVar(&cfg.timeout, "timeout", 3*time.Second, "overall timeout, greater than zero and at most 30s")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, 0
		}
		return cfg, 2
	}
	if err := cfg.validate(flags.NArg()); err != nil {
		fmt.Fprintln(stderr, err)
		return cfg, 2
	}
	return cfg, -1
}

func (cfg options) validate(positional int) error {
	if positional != 0 || cfg.socket == "" {
		return errors.New("status requires a socket path and no positional arguments")
	}
	if cfg.timeout <= 0 || cfg.timeout > 30*time.Second {
		return errors.New("status timeout must be greater than zero and at most 30s")
	}
	return nil
}
