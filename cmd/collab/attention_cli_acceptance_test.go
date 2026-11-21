package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func attentionCLISocket(t *testing.T, reply string) (string, <-chan map[string]any) {
	t.Helper()
	requests := make(chan map[string]any, 1)
	socket := statusSocket(t, func(c net.Conn) {
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err != nil {
			return
		}
		var request map[string]any
		if json.Unmarshal(line, &request) != nil {
			return
		}
		requests <- request
		_, _ = c.Write([]byte(reply + "\n"))
	})
	return socket, requests
}

func attentionCLIRun(t *testing.T, args []string) string {
	t.Helper()
	var out, stderr bytes.Buffer
	if code := run(context.Background(), args, &out, &stderr); code != 0 {
		t.Fatalf("exit%d: %s", code, stderr.String())
	}
	return out.String()
}

func attentionCLIRequest(t *testing.T, requests <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(time.Second):
		t.Fatal("CLI reported success without writing its operator request")
		return nil
	}
}

func attentionCLIFields(t *testing.T, request, expected map[string]any) {
	t.Helper()
	for key, want := range expected {
		if request[key] != want {
			t.Fatalf("request %s=%v want %v: %v", key, request[key], want, request)
		}
	}
}

func TestAttentionAcceptanceCLIControlsUseOperatorEndpoint(t *testing.T) {
	for _, operation := range []string{"on", "off", "status"} {
		t.Run(operation, func(t *testing.T) {
			socket, requests := attentionCLISocket(t, `{"type":"operator_attention_result","attention_state":{"agent_id":"quiet","dnd":true,"enforced":true,"pending":2,"inbox_used":3,"inbox_limit":32}}`)
			args := []string{"operator", "dnd", "--socket", socket, "--agent", "quiet", "--json"}
			if operation != "status" {
				args = append(args, operation)
			}
			out := attentionCLIRun(t, args)
			var rendered map[string]any
			if json.Unmarshal([]byte(out), &rendered) != nil {
				t.Fatalf("invalid JSON output: %s", out)
			}
			request := attentionCLIRequest(t, requests)
			attentionCLIFields(t, request, map[string]any{"type": "operator_attention", "agent_id": "quiet", "attention_op": operation})
		})
	}
}

func TestAttentionAcceptanceCLIUrgencyAndLiteralText(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, test := range []struct {
		name   string
		urgent bool
	}{{"ordinary", false}, {"urgent", true}} {
		t.Run(test.name, func(t *testing.T) {
			urgent := test.urgent
			socket, requests := attentionCLISocket(t, `{"type":"ack","stage":"accepted","message_id":"cli-attention","seq":42,"recipients":[{"agent_id":"quiet"}]}`)
			args := []string{"operator", "send", "--socket", socket, "--unsigned", "--json", "--message-id", "cli-attention", "--to", "quiet"}
			if urgent {
				args = append(args, "--urgent")
			}
			text := "literal $(not-a-command); --urgent"
			args = append(args, "--", text)
			attentionCLIRun(t, args)
			request := attentionCLIRequest(t, requests)
			gotUrgent, _ := request["urgent"].(bool)
			if gotUrgent != urgent {
				t.Fatalf("urgent=%v want%v", gotUrgent, urgent)
			}
			attentionCLIFields(t, request, map[string]any{"type": "operator_send", "to": "quiet", "message_id": "cli-attention"})
			payload, ok := request["payload"].(map[string]any)
			if !ok {
				t.Fatalf("payload is not an object: %v", request["payload"])
			}
			attentionCLIFields(t, payload, map[string]any{"text": text})
		})
	}
}

func TestAttentionAcceptanceCLIUnsupportedIsExplicit(t *testing.T) {
	socket, _ := attentionCLISocket(t, `{"type":"error","code":"unsupported_operation","detail":"broker does not support attention controls"}`)
	var out, stderr bytes.Buffer
	code := run(context.Background(), []string{"operator", "dnd", "--socket", socket, "--agent", "quiet", "on"}, &out, &stderr)
	if code == 0 {
		t.Fatal("unsupported broker reported success")
	}
	if !strings.Contains(strings.ToLower(out.String()+stderr.String()), "support") {
		t.Fatalf("unsupported reason missing: %s %s", out.String(), stderr.String())
	}
}
