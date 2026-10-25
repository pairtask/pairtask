package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/bridge"
)

// This endpoint exposes the existing listener, never another broker connection.
// Its private directory limits access to the local user's launched processes.
type runtimeMCP struct {
	dir    string
	ln     net.Listener
	cancel context.CancelFunc
	done   chan struct{}
	ready  func() bool
}

func newRuntimeMCP(ctx context.Context, listener *bridge.Listener, ready func() bool) (*runtimeMCP, error) {
	dir, err := os.MkdirTemp("/tmp", "collab-tools-")
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "mcp.sock"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	e := &runtimeMCP{dir: dir, ln: ln, cancel: cancel, done: make(chan struct{}), ready: ready}
	server := bridge.NewHostMCP(listener, false)
	server.AddReceivingMiddleware(e.requireReady)
	go e.serve(ctx, server)
	return e, nil
}

func (e *runtimeMCP) requireReady(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" || e.ready() {
			return next(ctx, method, req)
		}
		params, _ := req.GetParams().(*mcp.CallToolParamsRaw)
		if params != nil && params.Name == "listener_status" {
			return next(ctx, method, req)
		}
		return nil, &jsonrpc.Error{Code: -32000, Message: "managed thread is not ready; wait for start, resume, or fork to finish"}
	}
}

// runtimeTools are the collaboration tools the runtime MCP server serves.
var runtimeTools = []string{"send", "acknowledge", "receive", "wait_reply", "listener_status", "status", "read_status", "claim", "release_claim", "finish_claim", "read_claims"}

func (e *runtimeMCP) config() (map[string]any, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	// The five runtime tools are this launcher's own channel to the broker; a
	// Codex approval prompt for them would only ask the operator to approve
	// sending, receiving or acknowledging a message. They are pre-approved
	// ("approve": no confirmation), the counterpart of the pairtask tool
	// allowlist planned for Claude Code (#116); the server default stays
	// Codex's own, and every other approval (shell, files, network) stays as
	// configured.
	tools := map[string]any{}
	for _, name := range runtimeTools {
		tools[name] = map[string]any{"approval_mode": "approve"}
	}
	return map[string]any{"command": binary, "args": []string{"--mcp-socket", e.ln.Addr().String()}, "required": true, "enabled": true, "tools": tools}, nil
}

func (e *runtimeMCP) serve(ctx context.Context, server *mcp.Server) {
	defer close(e.done)
	var sessions sync.WaitGroup
	defer sessions.Wait()
	stop := context.AfterFunc(ctx, func() { e.ln.Close() })
	defer stop()
	for {
		conn, err := e.ln.Accept()
		if err != nil {
			return
		}
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			serveRuntimeMCP(ctx, server, conn)
		}()
	}
}

func serveRuntimeMCP(ctx context.Context, server *mcp.Server, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	err := server.Run(ctx, &mcp.IOTransport{Reader: conn, Writer: conn})
	if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
		return
	}
	if err != nil {
		log.Printf("runtime MCP connection: %v", err)
	}
}

func (e *runtimeMCP) Close() {
	e.cancel()
	e.ln.Close()
	<-e.done
	os.RemoveAll(e.dir)
}

// Codex launches this stdio relay as an MCP subprocess. It has no inbox ID and
// cannot independently register with the broker.
func relayMCP(ctx context.Context, socket string, stdio operatorIO) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close(); stdio.input.Close() })
	defer stop()
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, stdio.input)
		conn.Close()
		done <- err
	}()
	_, outputErr := io.Copy(stdio.output, conn)
	conn.Close()
	stdio.input.Close()
	inputErr := <-done
	if ctx.Err() != nil {
		return nil
	}
	return errors.Join(relayError(inputErr), relayError(outputErr))
}

func relayError(err error) error {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) {
		return nil // Closing either side intentionally interrupts the other copy.
	}
	return err
}
