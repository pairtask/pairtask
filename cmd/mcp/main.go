// Command mcp exposes the broker's messaging tools over MCP stdio.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/bridge"
	"github.com/pairtask/pairtask/internal/config"
	"github.com/pairtask/pairtask/internal/forge"
	"github.com/pairtask/pairtask/internal/forge/github"
	"github.com/pairtask/pairtask/internal/protocol"
)

func main() {
	socketDefault, err := config.Required(os.LookupEnv, "SOCKET_PATH", "/tmp/collab-ai.sock")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	socket := flag.String("socket", socketDefault, "broker Unix socket path")
	agent := flag.String("agent-id", "", "logical inbox ID; one active session per ID (required)")
	harness := flag.String("harness", "mcp", "agent harness name")
	model := flag.String("model", "", "agent model name (optional)")
	channel := flag.Bool("claude-channel", false, "advertise the experimental Claude channel; also requires Claude's explicit channel opt-in")
	autoListen := flag.Bool("auto-listen", false, "activate after MCP initialization; requires --claude-channel and a dedicated session configuration")
	var rooms protocol.RoomFlags
	flag.Var(&rooms, "room", "join this room on registration (repeatable)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := bridge.ClientConfig{SocketPath: *socket, AgentID: *agent, Harness: *harness, Model: *model, Rooms: rooms, Forges: reviewForges()}
	if err := run(ctx, cfg, *channel, *autoListen); err != nil {
		// stdout is reserved exclusively for MCP protocol frames.
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg bridge.ClientConfig, channel, autoListen bool) error {
	if autoListen && !channel {
		return fmt.Errorf("--auto-listen requires --claude-channel")
	}
	c, err := bridge.NewLazyClient(cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	var server *mcp.Server
	var transport mcp.Transport = &mcp.StdioTransport{}
	if channel {
		t := &bridge.ChannelTransport{Transport: transport}
		listener := bridge.NewListener(ctx, c, t)
		defer listener.Close()
		transport = t
		server = newChannelServer(listener, autoListen)
	} else {
		server = bridge.NewMCP(c)
	}
	err = server.Run(ctx, transport)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func newChannelServer(listener *bridge.Listener, autoListen bool) *mcp.Server {
	if autoListen {
		return bridge.NewAutoChannelMCP(listener)
	}
	return bridge.NewHostMCP(listener, true)
}

// reviewForges are the forges this agent's reviews may use (#201), with the
// agent's own access.
func reviewForges() forge.Registry {
	return forge.Registry{"github": github.New(github.Production())}
}
