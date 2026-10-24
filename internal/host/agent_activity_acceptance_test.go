package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/bridge"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestAgentActivityLauncherAttributesOnlyOwnedThreadEvents(t *testing.T) {
	cases := []struct {
		name, owned, params string
		want                bool
	}{
		{"owned", "owned-thread", `{"threadId":"owned-thread","turn":{"id":"turn-1"}}`, true},
		{"foreign", "owned-thread", `{"threadId":"foreign-thread","turn":{"id":"turn-1"}}`, false},
		{"missing-thread", "owned-thread", `{"turn":{"id":"turn-1"}}`, false},
		{"not-owned-yet", "", `{"threadId":"owned-thread","turn":{"id":"turn-1"}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _, down, ln := lifecycleProxy(t)
			p.threadID = tc.owned
			// Ordinary owned-thread mode must report too; budget mode is false.
			ready := make(chan error, 1)
			go func() { ready <- p.activateListener(context.Background()) }()
			conn := acceptOwner(t, ln)
			check(t, <-ready)
			check(t, p.FromHost(context.Background(), Frame{Method: "turn/started", Params: json.RawMessage(tc.params)}))
			if frameAt(t, down).Method != "turn/started" {
				t.Fatal("activity observation consumed the host event")
			}
			requireLauncherActivity(t, conn, tc.want)
			if p.Listener.Status().SessionID != "broker-session" {
				t.Fatal("activity changed broker owner")
			}
		})
	}
}

func requireLauncherActivity(t *testing.T, conn net.Conn, want bool) {
	t.Helper()
	deadline := time.Second
	if !want {
		deadline = 50 * time.Millisecond
	}
	check(t, conn.SetReadDeadline(time.Now().Add(deadline)))
	var pulse protocol.Message
	err := protocol.ReadFrame(bufio.NewReader(conn), &pulse)
	if !want {
		var timeout net.Error
		if !errors.As(err, &timeout) {
			t.Fatalf("unattributed event produced broker traffic: %+v, %v", pulse, err)
		}
		if !timeout.Timeout() {
			t.Fatalf("expected read timeout, got %v", err)
		}
		return
	}
	check(t, err)
	if pulse.Type != "agent_activity" || pulse.Source != "codex-launcher" {
		t.Fatalf("missing attributed launcher pulse: %+v", pulse)
	}
}

func TestAgentActivityBusyBrokerDoesNotBlockHostEvents(t *testing.T) {
	p, _, down, ln := lifecycleProxy(t)
	p.threadID = "owned-thread"
	ready := make(chan error, 1)
	go func() { ready <- p.activateListener(context.Background()) }()
	conn := acceptOwner(t, ln)
	check(t, <-ready)
	check(t, conn.(*net.UnixConn).SetReadBuffer(1024))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writing := make(chan error, 1)
	go func() {
		_, err := p.Listener.SendMessage(ctx, bridge.SendRequest{To: "peer", Text: strings.Repeat("x", 900<<10), MessageID: "busy-writer"})
		writing <- err
	}()
	// Reading one byte proves the ordinary message owns the writer; leave
	// the rest unread so telemetry has to contend with it.
	var first [1]byte
	_, err := conn.Read(first[:])
	check(t, err)
	select {
	case <-writing:
		t.Fatal("fixture did not hold the broker writer")
	default:
	}
	forwarded := make(chan error, 1)
	go func() {
		forwarded <- p.FromHost(context.Background(), Frame{Method: "turn/started", Params: json.RawMessage(`{"threadId":"owned-thread"}`)})
	}()
	if frameAt(t, down).Method != "turn/started" {
		t.Fatal("host event lost")
	}
	select {
	case err := <-forwarded:
		check(t, err)
	case <-time.After(250 * time.Millisecond):
		cancel()
		<-forwarded
		t.Fatal("best-effort activity blocked host processing behind the broker writer")
	}
	cancel()
	select {
	case <-writing:
	case <-time.After(time.Second):
		t.Fatal("blocked test writer did not stop")
	}
}
