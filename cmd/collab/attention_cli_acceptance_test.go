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
			if request["type"] != "operator_attention" || request["agent_id"] != "quiet" || request["attention_op"] != operation {
				t.Fatalf("control request: %v", request)
			}
		})
	}
}

func TestAttentionAcceptanceCLIUrgencyAndLiteralText(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, urgent := range []bool{false, true} {
		name := "ordinary"
		if urgent {
			name = "urgent"
		}
		t.Run(name, func(t *testing.T) {
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
			if request["type"] != "operator_send" || request["to"] != "quiet" || request["message_id"] != "cli-attention" {
				t.Fatalf("send identity: %v", request)
			}
			payload, ok := request["payload"].(map[string]any)
			if !ok || payload["text"] != text {
				t.Fatalf("literal text changed: %v", request["payload"])
			}
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
