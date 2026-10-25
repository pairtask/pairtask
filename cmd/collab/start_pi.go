package main

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
)

// piPlan starts pi with the pairtask extension. The extension reads the
// PAIRTASK_* environment and runs the bridge itself (#155).
func piPlan(opts startOptions, layout startLayout) startPlan {
	argv := []string{layout.PiBinary, "-e", layout.PiExtension}
	if opts.Model != "" {
		argv = append(argv, "--model", opts.Model)
	}
	return startPlan{Argv: append(argv, opts.Native...), Env: agentEnv(opts.ID, opts.Socket, opts.Rooms, layout.MCPBinary)}
}

// agentEnv is the adapter contract a harness reads to join pairtask: its
// agent ID, the broker socket, the rooms to join as a JSON array, and the
// bridge binary that serves the tools and pushes messages.
func agentEnv(id, socket string, rooms []string, mcp string) []string {
	if rooms == nil {
		rooms = []string{}
	}
	encoded, _ := json.Marshal(rooms)
	return []string{
		"PAIRTASK_AGENT_ID=" + id,
		"PAIRTASK_SOCKET=" + cmp.Or(socket, brokerSocketDefault),
		"PAIRTASK_ROOMS=" + string(encoded),
		"PAIRTASK_MCP=" + mcp,
	}
}

// withEnv is the environment with each of set replacing any inherited value
// of the same name, so a harness never sees two of them.
func withEnv(environ, set []string) []string {
	out := slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.ContainsFunc(set, func(s string) bool { return strings.HasPrefix(s, name+"=") })
	})
	return append(out, set...)
}
