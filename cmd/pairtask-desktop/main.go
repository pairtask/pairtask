// Command pairtask-desktop is the pairtask connector for Claude Desktop: a
// light node that joins other workspaces by their invitations and talks to
// them, served to Desktop as MCP tools on stdio. Logs go to stderr, which
// Desktop keeps; stdout carries only MCP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pairtask/pairtask/internal/desktop"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(os.Args[1:], log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// run dispatches the subcommand: daemon (the always-on agent), status
// (who runs the node, and its settings), agent
// (install it as a login item), or mcp, which is also what no subcommand
// means, so Claude Desktop's extension keeps working as it is.
func run(args []string, log *slog.Logger) error {
	switch first(args) {
	case "daemon":
		return runDaemon(args[1:], log)
	case "agent":
		return runAgent(args[1:], hostAgentEnv(), os.Stdout)
	case "status":
		return runStatus(args[1:], hostAgentEnv(), os.Stdout)
	case "mcp":
		args = args[1:]
	}
	return runMCP(args, log)
}

func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// daemonRetry is how often a waiting agent tries to take the data directory.
const daemonRetry = 2 * time.Second

// runDaemon runs the always-on agent until a signal stops it.
func runDaemon(args []string, log *slog.Logger) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}
	if cfg.Address != "" {
		return errors.New("daemon: --address is a setting of the MCP mode (Claude Desktop's Workspace address); the agent doesn't take it")
	}
	cfg.Log = log
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return desktop.RunDaemon(ctx, cfg, daemonRetry)
}

// runMCP starts the node in its data directory and serves MCP on stdio
// until the client closes it or a signal arrives: as the owner, or
// forwarding to the agent or another copy that owns the directory.
func runMCP(args []string, log *slog.Logger) error {
	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}
	cfg.Log = log
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	n, err := desktop.Start(ctx, cfg)
	if err != nil {
		return err
	}
	defer n.Close()
	go applyAddresses(ctx, n, cfg.Address, log)
	in := &clientInput{ReadCloser: os.Stdin}
	err = desktop.NewServer(n, version()).Run(ctx, &mcp.IOTransport{Reader: in, Writer: stdout{os.Stdout}})
	return sessionEnd(ctx, in, err, log)
}

// sessionEnd is how the MCP session's end counts: Desktop closing stdin or
// stopping the process is a normal end, whatever error the session ended
// with meanwhile; anything else is the error.
func sessionEnd(ctx context.Context, in *clientInput, err error, log *slog.Logger) error {
	if err != nil && stoppedByClient(ctx, in) {
		log.Info("session ended", "detail", err)
		return nil
	}
	return err
}

// applyAddresses moves the workspaces the Workspace address setting names,
// off the MCP path, since each move dials and probes; the log and
// pair_status say what happened.
func applyAddresses(ctx context.Context, n *desktop.Node, raw string, log *slog.Logger) {
	for _, o := range n.ApplyAddressSetting(ctx, raw) {
		log.Info("workspace address setting", "contact", o.Contact, "destination", o.Destination, "result", o.Result, "reason", o.Reason)
	}
}

// stoppedByClient: Desktop closed stdin, or a signal stopped the process.
func stoppedByClient(ctx context.Context, in *clientInput) bool {
	return in.closed.Load() || ctx.Err() != nil
}

// clientInput is stdin, noting when the client closed it.
type clientInput struct {
	io.ReadCloser
	closed atomic.Bool
}

func (in *clientInput) Read(p []byte) (int, error) {
	n, err := in.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		in.closed.Store(true)
	}
	return n, err
}

// stdout carries MCP and is never closed by the session.
type stdout struct{ io.Writer }

func (stdout) Close() error { return nil }

// parseFlags reads --dir, the private data directory (the user's config
// directory's pairtask-desktop by default), --name, the name to pair
// under, --notify, whether to show system notifications (on), and
// --address, the Workspace address setting.
func parseFlags(args []string) (desktop.Config, error) {
	fs := flag.NewFlagSet("pairtask-desktop", flag.ContinueOnError)
	var cfg desktop.Config
	fs.StringVar(&cfg.Dir, "dir", "", "private data directory (default: the user config directory's pairtask-desktop)")
	fs.StringVar(&cfg.Name, "name", "", "the name this node pairs under")
	notify := fs.Bool("notify", true, "show a system notification when a paired workspace sends a message (kept for the node)")
	fs.StringVar(&cfg.Address, "address", "", "where paired workspaces are now: HOST:PORT, or NAME=HOST:PORT, comma-separated")
	fs.StringVar(&cfg.Pairbook, "pairbook", "", "the company's Pairbook, https://HOST; empty for none")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if *notify {
		cfg.Notify = desktop.SystemNotifier() // shown while the stored setting is on
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "notify" {
			cfg.NotifyGiven = notify // kept for every copy of the node
		}
	})
	cfg.Version = version()
	if fs.NArg() > 0 {
		return cfg, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	dir, err := dataDir(cfg.Dir)
	cfg.Dir = dir
	return cfg, err
}

// dataDir is --dir, else the user config directory's pairtask-desktop.
func dataDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("no data directory: pass --dir: %w", err)
	}
	return filepath.Join(base, "pairtask-desktop"), nil
}

// version is the module version this binary was built from, or "dev".
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	switch info.Main.Version {
	case "", "(devel)":
		return "dev"
	}
	return info.Main.Version
}
