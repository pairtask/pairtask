//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func attentionAgentWrite(t *testing.T, p *operatorPeer, request any) {
	t.Helper()
	data, err := json.Marshal(request)
	operatorNoError(t, err)
	operatorNoError(t, p.SetWriteDeadline(time.Now().Add(2*time.Second)))
	_, err = p.Write(append(data, '\n'))
	operatorNoError(t, err)
}

func TestAttentionAcceptanceAgentCannotDisableDND(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	f.peer("quiet", 32)
	sender := f.peer("sender", 32)
	attentionSetting(f, "quiet", "on")
	attentionAgentWrite(t, sender, map[string]any{"type": "operator_attention", "agent_id": "quiet", "attention_op": "off"})
	response := attentionRead(t, sender)
	operatorEqual(t, "agent control refused", response.Type, "error")
	state := attentionSetting(f, "quiet", "status")
	operatorEqual(t, "agent cannot disable another agent's DND", state.State.DND, true)
}

func TestAttentionAcceptanceAgentCannotForgeUrgency(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	receiver := f.peer("quiet", 32)
	sender := f.peer("sender", 32)
	attentionSetting(f, "quiet", "on")
	attentionAgentWrite(t, sender, map[string]any{"type": "msg", "message_id": "forged-urgency", "to": "quiet", "durable": true, "ack_requested": true, "urgent": true, "attention": "urgent", "payload": map[string]string{"text": "ordinary agent text"}})
	response := attentionRead(t, sender)
	requireOperatorAccepted(t, response.Message, "quiet")
	delivered := attentionRead(t, receiver)
	operatorEqual(t, "sender remains agent", delivered.Origin, "agent")
	operatorEqual(t, "sender-controlled urgency overwritten", delivered.Attention, "suppressed")
}

func TestAttentionAcceptanceOperatorCannotForgeDisposition(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	receiver := f.peer("quiet", 32)
	attentionSetting(f, "quiet", "on")
	response := attentionCall(f, map[string]any{"type": "operator_send", "message_id": "forged-disposition", "to": "quiet", "attention": "urgent", "payload": map[string]string{"text": "ordinary operator text"}})
	requireOperatorAccepted(t, response.Message, "quiet")
	delivered := attentionRead(t, receiver)
	operatorEqual(t, "operator provenance preserved", delivered.Origin, "operator")
	operatorEqual(t, "disposition does not substitute for urgent request", delivered.Attention, "suppressed")
}

func TestAttentionAcceptanceSuppressedInboxBound(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	receiver := f.peer("quiet", 32)
	attentionSetting(f, "quiet", "on")
	for i := 0; i < 32; i++ {
		id := fmt.Sprintf("attention-bound-%02d", i)
		requireOperatorAccepted(t, f.send(operatorRequest(id, "quiet")), "quiet")
		message := attentionRead(t, receiver)
		operatorEqual(t, "every admitted message delivered quietly", message.Attention, "suppressed")
		operatorEqual(t, "every message ID preserved", message.MessageID, id)
	}
	state := attentionSetting(f, "quiet", "status")
	operatorEqual(t, "suppressed pending bounded", state.State.Pending, 32)
	operatorEqual(t, "actual inbox occupancy", state.State.InboxUsed, 32)
	refused := f.send(operatorRequest("attention-overflow", "quiet"))
	operatorEqual(t, "overflow explicit", refused.Code, "inbox_full")
	state = attentionSetting(f, "quiet", "status")
	operatorEqual(t, "overflow cannot silently acknowledge", state.State.Pending, 32)
	operatorEqual(t, "overflow cannot displace old messages", state.State.InboxUsed, 32)
}
