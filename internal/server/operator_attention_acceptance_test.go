//go:build linux

// Local attention contract agreed in peer review 25982a12 and operator 149f9bc4.
package server

import (
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type attentionAcceptanceFrame struct {
	protocol.Message
	Attention string `json:"attention"`
	State     *struct {
		AgentID    string `json:"agent_id"`
		DND        bool   `json:"dnd"`
		Enforced   bool   `json:"enforced"`
		Reason     string `json:"reason"`
		Pending    int    `json:"pending"`
		InboxUsed  int    `json:"inbox_used"`
		InboxLimit int    `json:"inbox_limit"`
	} `json:"attention_state"`
}

func attentionRead(t *testing.T, p *operatorPeer) attentionAcceptanceFrame {
	t.Helper()
	operatorNoError(t, p.SetReadDeadline(time.Now().Add(2*time.Second)))
	line, err := p.r.ReadBytes('\n')
	operatorNoError(t, err)
	var out attentionAcceptanceFrame
	operatorNoError(t, json.Unmarshal(line, &out))
	return out
}

func attentionCall(f *operatorFixture, request any) attentionAcceptanceFrame {
	f.t.Helper()
	p := operatorDial(f.t, f.operator)
	operatorNoError(f.t, p.SetDeadline(time.Now().Add(2*time.Second)))
	data, err := json.Marshal(request)
	operatorNoError(f.t, err)
	_, err = p.Write(append(data, '\n'))
	operatorNoError(f.t, err)
	out := attentionRead(f.t, p)
	_, err = p.r.ReadByte()
	operatorEqual(f.t, "one response per operator connection", err, io.EOF)
	return out
}

func attentionSetting(f *operatorFixture, agent, operation string) attentionAcceptanceFrame {
	f.t.Helper()
	r := attentionCall(f, map[string]any{"type": "operator_attention", "agent_id": agent, "attention_op": operation})
	operatorEqual(f.t, "attention result", r.Type, "operator_attention_result")
	if r.State == nil {
		f.t.Fatal("attention result omitted its state")
	}
	operatorEqual(f.t, "selected logical agent", r.State.AgentID, agent)
	operatorEqual(f.t, "explicit inbox limit", r.State.InboxLimit, 32)
	return r
}

func TestAttentionAcceptanceOrdinaryControl(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	receiver := f.peer("worker", 31)
	requireOperatorAccepted(t, f.send(operatorRequest("attention-control", "worker")), "worker")
	r := attentionRead(t, receiver)
	operatorEqual(t, "ordinary ID", r.MessageID, "attention-control")
	operatorEqual(t, "ordinary origin", r.Origin, "operator")
	operatorEqual(t, "legacy notification handling", r.Attention, "")
}

func TestAttentionAcceptanceSuppressionAndScope(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	quiet := f.peer("quiet", 32)
	loud := f.peer("loud", 32)
	state := attentionSetting(f, "quiet", "on")
	operatorEqual(t, "DND enabled", state.State.DND, true)
	operatorEqual(t, "capable adapter enforces DND", state.State.Enforced, true)
	requireOperatorAccepted(t, f.send(operatorRequest("attention-quiet", "quiet")), "quiet")
	requireOperatorAccepted(t, f.send(operatorRequest("attention-loud", "loud")), "loud")
	q := attentionRead(t, quiet)
	l := attentionRead(t, loud)
	operatorEqual(t, "quiet message delivered", q.MessageID, "attention-quiet")
	operatorEqual(t, "quiet message notification", q.Attention, "suppressed")
	operatorEqual(t, "other agent delivered", l.MessageID, "attention-loud")
	operatorEqual(t, "other agent unaffected", l.Attention, "")
	state = attentionSetting(f, "quiet", "status")
	operatorEqual(t, "delivery is not acknowledgment", state.State.Pending, 1)
	operatorEqual(t, "suppressed message occupies the inbox", state.State.InboxUsed, 1)
}

func TestAttentionAcceptanceLocalOperatorUrgent(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	receiver := f.peer("worker", 32)
	attentionSetting(f, "worker", "on")
	r := attentionCall(f, map[string]any{"type": "operator_send", "message_id": "attention-urgent", "to": "worker", "urgent": true, "payload": map[string]string{"text": "urgent operator message"}})
	requireOperatorAccepted(t, r.Message, "worker")
	delivered := attentionRead(t, receiver)
	operatorEqual(t, "urgent message delivered", delivered.MessageID, "attention-urgent")
	operatorEqual(t, "urgent provenance", delivered.Origin, "operator")
	operatorEqual(t, "urgent bypass disposition", delivered.Attention, "urgent")
	state := attentionSetting(f, "worker", "status")
	operatorEqual(t, "urgent send does not disable DND", state.State.DND, true)
	operatorEqual(t, "urgent message is not suppressed pending", state.State.Pending, 0)
	operatorEqual(t, "urgent message still occupies the inbox", state.State.InboxUsed, 1)
}

func TestAttentionAcceptanceOlderAdapterHonesty(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	receiver := f.peer("legacy", 31)
	state := attentionSetting(f, "legacy", "on")
	operatorEqual(t, "stored setting", state.State.DND, true)
	operatorEqual(t, "older adapter cannot enforce", state.State.Enforced, false)
	if state.State.Reason == "" {
		t.Fatal("unsupported enforcement needs an explicit reason")
	}
	requireOperatorAccepted(t, f.send(operatorRequest("attention-legacy", "legacy")), "legacy")
	delivered := attentionRead(t, receiver)
	operatorEqual(t, "legacy delivery retained", delivered.MessageID, "attention-legacy")
	operatorEqual(t, "do not claim suppression", delivered.Attention, "")
}

func TestAttentionAcceptanceUrgencyIsFrozenOnRetry(t *testing.T) {
	for _, firstUrgent := range []bool{false, true} {
		name := "ordinary to urgent"
		if firstUrgent {
			name = "urgent to ordinary"
		}
		t.Run(name, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			receiver := f.peer("worker", 32)
			request := map[string]any{"type": "operator_send", "message_id": "attention-retry", "to": "worker", "urgent": firstUrgent, "payload": map[string]string{"text": "same content"}}
			first := attentionCall(f, request)
			requireOperatorAccepted(t, first.Message, "worker")
			attentionRead(t, receiver)
			repeated := attentionCall(f, request)
			requireOperatorAccepted(t, repeated.Message, "worker")
			operatorEqual(t, "identical retry remains idempotent", repeated.Repeated, true)
			operatorEqual(t, "identical retry preserves sequence", repeated.Seq, first.Seq)
			request["urgent"] = !firstUrgent
			changed := attentionCall(f, request)
			operatorEqual(t, "changed urgency is a changed request", changed.Code, "duplicate_message")
			operatorEqual(t, "retry cannot persist another message", f.count("messages"), 1)
		})
	}
}
