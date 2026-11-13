package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type operatorWireScope struct {
	args       []string
	to         string
	groupCount int // -1 leaves membership unconstrained for broadcast.
}

func TestOperatorAcceptanceCLIWire(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	scopes := []operatorWireScope{
		{[]string{"--to", "alpha"}, "alpha", 0},
		{[]string{"--group", "beta,alpha"}, "", 2},
		{[]string{"--all"}, "*", -1},
	}
	for _, scope := range scopes {
		t.Run(scope.args[0], func(t *testing.T) { checkOperatorCLIWire(t, scope) })
	}
}

func checkOperatorCLIWire(t *testing.T, scope operatorWireScope) {
	t.Helper()
	requests := make(chan protocol.Message, 1)
	socket := statusSocket(t, func(c net.Conn) {
		var m protocol.Message
		if protocol.ReadFrame(bufio.NewReader(c), &m) != nil {
			return
		}
		requests <- m
		protocol.WriteFrame(c, protocol.Message{Type: "ack", Stage: "accepted", MessageID: m.MessageID, Seq: 42, Recipients: []protocol.Recipient{{AgentID: "alpha"}}})
	})
	args := append([]string{"operator", "send", "--socket", socket, "--json", "--message-id", "cli-id"}, scope.args...)
	args = append(args, "hello operator")
	var out, stderr bytes.Buffer
	if code := run(context.Background(), args, &out, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("invalid JSON output: %s", out.String())
	}
	select {
	case m := <-requests:
		requireOperatorCLIContent(t, m)
		requireOperatorCLIScope(t, m, scope)
	case <-time.After(time.Second):
		t.Fatal("no operator request")
	}
}

func requireOperatorCLIContent(t *testing.T, m protocol.Message) {
	t.Helper()
	if m.Type != "operator_send" || m.MessageID != "cli-id" {
		t.Fatalf("wire: %+v", m)
	}
	var body struct {
		Text string `json:"text"`
	}
	if e := json.Unmarshal(m.Payload, &body); e != nil || body.Text != "hello operator" {
		t.Fatalf("payload: %s %v", m.Payload, e)
	}
}

func requireOperatorCLIScope(t *testing.T, m protocol.Message, scope operatorWireScope) {
	t.Helper()
	if m.To != scope.to {
		t.Fatalf("%s recipient: %+v", scope.args[0], m)
	}
	if scope.groupCount >= 0 && len(m.Group) != scope.groupCount {
		t.Fatalf("%s membership: %+v", scope.args[0], m)
	}
}

func TestOperatorAcceptanceCLIInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"--to", "a", "--all", "text"}, {"--to", "a", "--group", "b", "text"}, {"--to", "a", "--group", "", "text"}, {"--group", "", "text"}, {"--group", "a,,b", "text"}, {"--to", "a"}, {"text"}} {
		var out, stderr bytes.Buffer
		argv := append([]string{"operator", "send", "--socket", filepath.Join(t.TempDir(), "absent.sock")}, args...)
		if code := run(context.Background(), argv, &out, &stderr); code != 2 {
			t.Fatalf("invalid args %v: exit %d, stderr %s", args, code, stderr.String())
		}
		if out.Len() != 0 {
			t.Fatal("usage error polluted stdout")
		}
	}
}
func TestOperatorAcceptanceCLIGeneratesIDAndReportsRejection(t *testing.T) {
	requests := make(chan protocol.Message, 1)
	socket := statusSocket(t, func(c net.Conn) {
		var m protocol.Message
		if protocol.ReadFrame(bufio.NewReader(c), &m) != nil {
			return
		}
		requests <- m
		protocol.WriteFrame(c, protocol.Message{Type: "error", Code: "forbidden", Detail: "UID rejected"})
	})
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"operator", "send", "--socket", socket, "--to", "alpha", "hello"}, &out, &stderr); code != 1 {
		t.Fatalf("rejection exit %d: %s", code, stderr.String())
	}
	select {
	case m := <-requests:
		if m.MessageID == "" {
			t.Fatal("generated ID missing")
		}
	case <-time.After(time.Second):
		t.Fatal("no request")
	}
}

func TestOperatorAcceptanceCLIQuotesTerminalControls(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, reply := range []protocol.Message{{Type: "ack", Stage: "accepted", Seq: 1, MessageID: "id\x1b[2J", Recipients: []protocol.Recipient{{AgentID: "agent\x1b[2J"}}, Excluded: []string{"old\x1b[2J"}}, {Type: "error", Code: "unknown_recipient", Detail: "missing agent\x1b[2J"}} {
		socket := statusSocket(t, func(c net.Conn) {
			var m protocol.Message
			if protocol.ReadFrame(bufio.NewReader(c), &m) != nil {
				return
			}
			protocol.WriteFrame(c, reply)
		})
		var out, stderr bytes.Buffer
		run(context.Background(), []string{"operator", "send", "--socket", socket, "--to", "alpha", "hello"}, &out, &stderr)
		if strings.ContainsRune(out.String()+stderr.String(), '\x1b') {
			t.Fatal("operator CLI emitted terminal control bytes from broker fields")
		}
	}
}
