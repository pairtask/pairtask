package main

import (
	"context"
	"flag"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pairtask/pairtask/internal/bridge"
	"github.com/pairtask/pairtask/internal/desktop"
)

// agentFlags are mcp's flags for running as one of this Mac's agents.
type agentFlags struct {
	id      string
	channel bool
}

func (a *agentFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&a.id, "agent-id", "", "run as this Mac's agent ID (e.g. claude-desktop, codex): its own inbox, rooms, mentions and replies, as a Box agent; empty runs as the Mac's owner")
	fs.BoolVar(&a.channel, "claude-channel", false, "advertise Claude Code's channel, so new messages wake the session; also needs Claude's own channel opt-in")
}

// runAsAgent serves agent's MCP tools on transport: the same tools a Box
// agent has, registered with this Mac's node at its agent socket. The node
// itself is kept running by this process's copy (it owns it, or forwards
// to the copy that does), and the client reconnects when another copy
// takes the node over, after one call reports the gap.
func runAsAgent(ctx context.Context, dir string, agent agentFlags, transport mcp.Transport) error {
	c, err := bridge.NewRedialingClient(bridge.ClientConfig{SocketPath: desktop.AgentSocketPath(dir), AgentID: agent.id, Harness: "pairtask-desktop"})
	if err != nil {
		return err
	}
	defer c.Close()
	if !agent.channel {
		return bridge.NewMCP(c).Run(ctx, transport)
	}
	t := &bridge.ChannelTransport{Transport: transport}
	listener := bridge.NewListener(ctx, c, t)
	defer listener.Close()
	return bridge.NewHostMCP(listener, true).Run(ctx, t)
}
