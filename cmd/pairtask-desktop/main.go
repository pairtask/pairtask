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

// run starts the node in its data directory and serves MCP on stdio until
// Desktop closes it or a signal arrives.
func run(args []string, log *slog.Logger) error {
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
// directory's pairtask-desktop by default), and --name, the name to pair
// under.
func parseFlags(args []string) (desktop.Config, error) {
	fs := flag.NewFlagSet("pairtask-desktop", flag.ContinueOnError)
	var cfg desktop.Config
	fs.StringVar(&cfg.Dir, "dir", "", "private data directory (default: the user config directory's pairtask-desktop)")
	fs.StringVar(&cfg.Name, "name", "", "the name this node pairs under")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if fs.NArg() > 0 {
		return cfg, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if cfg.Dir != "" {
		return cfg, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return cfg, fmt.Errorf("no data directory: pass --dir: %w", err)
	}
	cfg.Dir = filepath.Join(base, "pairtask-desktop")
	return cfg, nil
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
