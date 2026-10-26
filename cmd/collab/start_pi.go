package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// piPlan starts pi with the pairtask extension. The extension reads the
// PAIRTASK_* environment and runs the bridge itself (#155).
func piPlan(opts startOptions, layout startLayout) startPlan {
	argv := []string{layout.PiBinary, "-e", layout.PiExtension}
	if opts.Model != "" {
		argv = append(argv, "--model", opts.Model)
	}
	env := append(agentEnv(opts.ID, opts.Socket, opts.Rooms, layout.MCPBinary), "PAIRTASK_PI_MAX_TOKENS="+opts.MaxTokens.String())
	return startPlan{Argv: append(argv, opts.Native...), Env: env}
}

// defaultOutputCap bounds what one request may ask for. Providers that
// reserve the worst case up front (OpenRouter) refuse a request whose
// maximum output would cost more than the remaining credit, and Pi otherwise
// asks for nearly the whole context window (#170).
const (
	defaultOutputCap = 32768
	minOutputCap     = 2048      // room for the smallest thinking budget plus an answer
	maxOutputCap     = 1<<53 - 1 // the extension reads it as a JavaScript safe integer
)

// outputCap is --max-tokens: unset means the default, 0 no cap.
type outputCap struct {
	n   int
	set bool
}

func (c *outputCap) String() string {
	if !c.set {
		return strconv.Itoa(defaultOutputCap)
	}
	return strconv.Itoa(c.n)
}

func (c *outputCap) Set(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || !validOutputCap(n) {
		return fmt.Errorf("--max-tokens %q: 0 for no cap, or from %d to %d", v, minOutputCap, maxOutputCap)
	}
	c.n, c.set = n, true
	return nil
}

// validOutputCap is 0, or within [minOutputCap, maxOutputCap].
func validOutputCap(n int) bool {
	if n == 0 {
		return true
	}
	return n >= minOutputCap && n <= maxOutputCap
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
