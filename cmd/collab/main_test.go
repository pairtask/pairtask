package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func statusSocket(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "collab-status-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "broker.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		handle(conn)
	}()
	t.Cleanup(func() { ln.Close(); <-done })
	return path
}

func readyStatus() protocol.StatusSnapshot {
	out := protocol.UnavailableStatus()
	now, zero := time.Now().UTC(), 0
	out.Reachable, out.Health, out.HistoryAvailable = true, "ready", true
	out.SnapshotAt, out.BrokerStartedAt = &now, &now
	out.ConnectedSessions, out.ProtocolVersion = &zero, protocol.Version
	out.DurablePending = &protocol.PendingStatus{Recipients: []protocol.PendingRecipient{}, RecipientLimit: protocol.StatusLimit}
	return out
}

func runJSON(t *testing.T, args ...string) (int, map[string]any) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), append([]string{"status", "--json"}, args...), &stdout, &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("diagnostics mixed into JSON command: %s", &stderr)
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %s: %v", &stdout, err)
	}
	return code, out
}

func assertStatusEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func assertJSONField(t *testing.T, out map[string]any, key string, want any) {
	t.Helper()
	got, ok := out[key]
	if !ok {
		t.Fatalf("missing JSON field %q: %+v", key, out)
	}
	assertStatusEqual(t, key, got, want)
}

func TestStatusJSONUnavailableUsesNullCounts(t *testing.T) {
	code, out := runJSON(t, "--socket", filepath.Join(t.TempDir(), "missing.sock"))
	assertStatusEqual(t, "exit code", code, 1)
	assertJSONField(t, out, "reachable", false)
	assertJSONField(t, out, "health", "unavailable")
	for _, key := range []string{"connected_sessions", "durable_pending", "legacy_unacknowledged", "snapshot_at"} {
		assertJSONField(t, out, key, nil)
	}
	assertJSONField(t, out, "schema_version", float64(protocol.StatusSchemaVersion))
}

func TestStatusJSONUsesReadOnlyRequestAndSocketEnvironment(t *testing.T) {
	request := make(chan protocol.Message, 1)
	socket := statusSocket(t, func(conn net.Conn) {
		var msg protocol.Message
		if err := protocol.ReadFrame(bufio.NewReader(conn), &msg); err != nil {
			return
		}
		request <- msg
		out := readyStatus()
		protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeStatus, Status: &out})
	})
	t.Setenv("PAIRTASK_SOCKET_PATH", socket)
	code, out := runJSON(t)
	assertStatusEqual(t, "exit code", code, 0)
	assertJSONField(t, out, "reachable", true)
	assertJSONField(t, out, "connected_sessions", float64(0))
	msg := <-request
	assertStatusEqual(t, "request type", msg.Type, protocol.TypeStatus)
	assertStatusEqual(t, "request agent ID", msg.AgentID, "")
	if len(out["sessions"].([]any)) != 0 {
		t.Fatal("empty sessions must be an array")
	}
}

func TestStatusLegacyAndInvalidBrokersDoNotFallBackToRegistration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reply  protocol.Message
		health string
	}{
		{"legacy", protocol.Message{Type: protocol.TypeError, Code: protocol.ErrExpectedHello}, "unsupported"},
		{"wrong frame", protocol.Message{Type: protocol.TypeWelcome}, "unavailable"},
		{"unknown schema", protocol.Message{Type: protocol.TypeStatus, Status: &protocol.StatusSnapshot{SchemaVersion: 99}}, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket := statusSocket(t, func(conn net.Conn) {
				r := bufio.NewReader(conn)
				var msg protocol.Message
				if err := protocol.ReadFrame(r, &msg); err != nil {
					return
				}
				protocol.WriteFrame(conn, tc.reply)
				if err := protocol.ReadFrame(r, &msg); err == nil {
					t.Error("client retried with a messaging handshake")
				}
			})
			code, out := runJSON(t, "--socket", socket)
			assertStatusEqual(t, "exit code", code, 1)
			assertJSONField(t, out, "reachable", true)
			assertJSONField(t, out, "health", tc.health)
			assertJSONField(t, out, "durable_pending", nil)
		})
	}
}

func TestStatusTimeoutAndCancellation(t *testing.T) {
	socket := statusSocket(t, func(conn net.Conn) {
		var msg protocol.Message
		r := bufio.NewReader(conn)
		protocol.ReadFrame(r, &msg)
		protocol.ReadFrame(r, &msg) // Wait for the timed-out client to close.
	})
	start := time.Now()
	code, out := runJSON(t, "--socket", socket, "--timeout", "50ms")
	assertStatusEqual(t, "exit code", code, 1)
	assertJSONField(t, out, "reachable", true)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout not honored: %s", elapsed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if run(ctx, []string{"status", "--socket", socket}, &stdout, &stderr) != 1 {
		t.Fatal("cancelled query succeeded")
	}
}

func TestStatusHumanOutputEscapesIDsAndExplainsLimits(t *testing.T) {
	socket := statusSocket(t, func(conn net.Conn) {
		var msg protocol.Message
		protocol.ReadFrame(bufio.NewReader(conn), &msg)
		out := readyStatus()
		one := 1
		out.ConnectedSessions = &one
		out.Sessions = []protocol.SessionStatus{{AgentID: "agent\n\x1b[2J", SessionID: "session", State: "transport_connected", ConnectedAt: *out.SnapshotAt}}
		protocol.WriteFrame(conn, protocol.Message{Type: protocol.TypeStatus, Status: &out})
	})
	var stdout, stderr bytes.Buffer
	if run(context.Background(), []string{"status", "--socket", socket}, &stdout, &stderr) != 0 {
		t.Fatal(stderr.String())
	}
	text := stdout.String()
	for _, want := range []string{"Broker: ready", "Connected sessions: 1", "Durable pending recipient deliveries: 0", "transport_connected", "limit 100", "unavailable", `agent\n\x1b[2J`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if strings.ContainsRune(text, '\x1b') {
		t.Fatal("terminal escape injection")
	}
}

func TestStatusInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"status", "--timeout", "0s"}, {"status", "--timeout", "31s"}, {"status", "extra"}} {
		var stdout, stderr bytes.Buffer
		if run(context.Background(), args, &stdout, &stderr) != 2 {
			t.Fatalf("accepted arguments: %v", args)
		}
		if stdout.Len() != 0 {
			t.Fatal("usage error polluted stdout")
		}
	}
}
