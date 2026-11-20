package chat_test

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/chat"
	"github.com/pairtask/pairtask/internal/protocol"
)

func attentionChatFields(t *testing.T, message protocol.Message) map[string]any {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func attentionChatResponse(t *testing.T, wire string) protocol.Message {
	t.Helper()
	var result protocol.Message
	if err := json.Unmarshal([]byte(wire), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAttentionAcceptanceChatDNDDoesNotConsumeMessageDraft(t *testing.T) {
	for _, operation := range []string{"on", "off", "status"} {
		t.Run(operation, func(t *testing.T) {
			h := commandCompleteHarness(t)
			v2Type(h, "unfinished ordinary message")
			line := "/dnd @alice"
			if operation != "status" {
				line += " " + operation
			}
			v2Command(h, line)
			request := h.take("operator_attention")
			fields := attentionChatFields(t, request)
			chatEqual(t, "explicit local agent", fields["agent_id"], "alice")
			gotOperation, _ := fields["attention_op"].(string)
			chatEqual(t, "DND operation", gotOperation, operation)
			chatEqual(t, "DND does not consume the composer", h.m.State().Draft, "unfinished ordinary message")
			h.reply(request, attentionChatResponse(t, `{"type":"operator_attention_result","attention_state":{"agent_id":"alice","dnd":true,"enforced":true,"pending":2,"inbox_used":3,"inbox_limit":32}}`))
			h.await(func() bool { return h.m.State().InFlight == 0 })
			chatEqual(t, "control response preserves composer", h.m.State().Draft, "unfinished ordinary message")
		})
	}
}

func TestAttentionAcceptanceChatUrgencyIsOneExplicitLocalSend(t *testing.T) {
	h := commandCompleteHarness(t)
	v2Command(h, "/urgent @alice one urgent message")
	request := h.take(protocol.TypeOperatorSend)
	fields := attentionChatFields(t, request)
	chatEqual(t, "explicit urgent recipient", request.To, "alice")
	chatEqual(t, "no implicit group", len(request.Group), 0)
	chatEqual(t, "one send marked urgent", fields["urgent"], true)
	var payload struct{ Text string }
	if err := json.Unmarshal(request.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	chatEqual(t, "urgent text", payload.Text, "one urgent message")
	h.reply(request, protocol.Message{Type: "ack", Stage: "accepted", MessageID: request.MessageID, Seq: 20, Recipients: []protocol.Recipient{{AgentID: "alice"}}})
	h.await(func() bool { return h.m.State().Intent.State == chat.IntentAccepted })
	h.draft("alice", "ordinary follow-up")
	h.input(chatKey(tea.KeyEnter, 0))
	next := h.take(protocol.TypeOperatorSend)
	urgent, _ := attentionChatFields(t, next)["urgent"].(bool)
	chatEqual(t, "urgency does not stick to next draft", urgent, false)
}

func TestAttentionAcceptanceChatRejectsRemoteAndBroadAttentionTargets(t *testing.T) {
	for _, line := range []string{"/dnd @all on", "/dnd @alice@remote on", "/dnd @alice @bob on", "/dnd @alice later", "/urgent @all hello", "/urgent @alice@remote hello", "/urgent @alice"} {
		t.Run(line, func(t *testing.T) {
			h := commandCompleteHarness(t)
			v2Type(h, "unfinished message")
			v2Command(h, line)
			chatEqual(t, "invalid attention command sends nothing", h.m.State().InFlight, 0)
			chatEqual(t, "invalid command preserves composer", h.m.State().Draft, "unfinished message")
		})
	}
}

func TestAttentionAcceptanceChatShowsUnsupportedBroker(t *testing.T) {
	h := commandCompleteHarness(t)
	v2Command(h, "/dnd @alice on")
	request := h.take("operator_attention")
	h.reply(request, protocol.Message{Type: "error", Code: "unsupported_operation", Detail: "broker does not support attention controls"})
	h.await(func() bool { return h.m.State().InFlight == 0 })
	if !strings.Contains(strings.ToLower(h.m.State().Status), "support") {
		t.Fatalf("unsupported reason missing: %s", h.m.State().Status)
	}
}
