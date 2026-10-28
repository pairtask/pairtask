package main

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pairtask/pairtask/internal/bridge"
)

func runtimeFixture(t *testing.T) (*runtimeMCP, *bridge.Listener) {
	t.Helper()
	client, err := bridge.NewLazyClient(bridge.ClientConfig{AgentID: "runtime-test", SocketPath: "/unused"})
	terminalCheck(t, err)
	listener := bridge.NewListener(context.Background(), client, nil)
	t.Cleanup(listener.Close)
	endpoint, err := newRuntimeMCP(context.Background(), listener, func() bool { return false })
	terminalCheck(t, err)
	t.Cleanup(endpoint.Close)
	return endpoint, listener
}

func TestRuntimeMCPRejectsInboxCallsBeforeThreadSelection(t *testing.T) {
	endpoint, listener := runtimeFixture(t)
	session := runtimeClient(t, endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "listen", Arguments: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "managed thread is not ready") {
		t.Fatalf("early inbox call was not blocked: %v", err)
	}
	if listener.Status().State != "inactive" || listener.Status().Error != "" {
		t.Fatal("early tool call attempted broker registration")
	}
}

func runtimeClient(t *testing.T, endpoint *runtimeMCP) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", endpoint.ln.Addr().String())
	terminalCheck(t, err)
	t.Cleanup(func() { conn.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "runtime-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: conn, Writer: conn}, nil)
	terminalCheck(t, err)
	t.Cleanup(func() { session.Close() })
	return session
}

func TestRuntimeMCPDiscoveryIsPassiveAcrossConnections(t *testing.T) {
	endpoint, listener := runtimeFixture(t)
	info, err := os.Stat(endpoint.dir)
	terminalCheck(t, err)
	if info.Mode().Perm() != 0700 {
		t.Fatal("runtime tool socket is not in a private directory")
	}
	for range 2 {
		session := runtimeClient(t, endpoint)
		tools, err := session.ListTools(context.Background(), nil)
		terminalCheck(t, err)
		if len(tools.Tools) != 23 { // collaboration, status, claims, three room and seven review tools
			t.Fatalf("missing runtime tools: %v", tools.Tools)
		}
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "listener_status"})
		terminalCheck(t, err)
		if result.IsError {
			t.Fatalf("listener status failed: %v", result)
		}
	}
	if listener.Status().State != "inactive" {
		t.Fatal("MCP discovery or status registered an inbox")
	}
}

func TestRuntimeMCPCloseStopsClientsAndRemovesSocket(t *testing.T) {
	endpoint, _ := runtimeFixture(t)
	session := runtimeClient(t, endpoint)
	endpoint.Close()
	if _, err := os.Stat(endpoint.dir); !os.IsNotExist(err) {
		t.Fatalf("runtime directory survived shutdown: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := session.ListTools(ctx, nil); err == nil {
		t.Fatal("MCP connection survived endpoint shutdown")
	}
}

func TestMCPStdioRelayForwardsToolsAndStopsOnCancellation(t *testing.T) {
	endpoint, _ := runtimeFixture(t)
	stdio, clientIO := net.Pipe()
	defer clientIO.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- relayMCP(ctx, endpoint.ln.Addr().String(), operatorIO{input: stdio, output: stdio}) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "relay-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: clientIO, Writer: clientIO}, nil)
	terminalCheck(t, err)
	defer session.Close()
	_, err = session.ListTools(ctx, nil)
	terminalCheck(t, err)
	cancel()
	select {
	case err := <-done:
		terminalCheck(t, err)
	case <-time.After(time.Second):
		t.Fatal("stdio relay leaked a copy goroutine on cancellation")
	}
}
