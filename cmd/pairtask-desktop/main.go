// Command pairtask-desktop is the pairtask connector for Claude Desktop: a
// light node that joins other workspaces by their invitations and talks to
// them, served to Desktop as MCP tools on stdio. Logs go to stderr, which
// Desktop keeps; stdout carries only MCP.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
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
	return desktop.NewServer(n, version()).Run(ctx, &mcp.StdioTransport{})
}

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
