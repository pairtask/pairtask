//go:build linux

package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func attentionAdvisories(t *testing.T, response protocol.Message) map[string]string {
	t.Helper()
	data, err := json.Marshal(response)
	operatorNoError(t, err)
	var decoded struct {
		Recipients []struct {
			AgentID   string `json:"agent_id"`
			Attention string `json:"attention"`
		} `json:"recipients"`
	}
	operatorNoError(t, json.Unmarshal(data, &decoded))
	result := map[string]string{}
	for _, recipient := range decoded.Recipients {
		result[recipient.AgentID] = recipient.Attention
	}
	return result
}

func TestAttentionAcceptanceGroupAdvisoryReportsActualEnforcement(t *testing.T) {
	database := filepath.Join(t.TempDir(), "advisory.db")
	if !t.Run("register offline recipient", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		f.peer("offline", 32)
		attentionSetting(f, "offline", "on")
	}) {
		return
	}
	f := newOperatorFixture(t, os.Getuid(), database)
	quiet := f.peer("quiet", 32)
	legacy := f.peer("legacy", 31)
	loud := f.peer("loud", 32)
	attentionSetting(f, "quiet", "on")
	attentionSetting(f, "legacy", "on")
	request := operatorRequest("advisory-group", "")
	request.Group = []string{"quiet", "legacy", "loud", "offline"}
	accepted := f.send(request)
	requireOperatorAccepted(t, accepted, "legacy", "loud", "offline", "quiet")
	want := map[string]string{"quiet": "suppressed", "legacy": "unsupported", "loud": "", "offline": "offline"}
	if got := attentionAdvisories(t, accepted); !reflect.DeepEqual(got, want) {
		t.Fatalf("per-recipient advisory %v want %v", got, want)
	}
	operatorEqual(t, "capable recipient quiet", attentionRead(t, quiet).Attention, "suppressed")
	operatorEqual(t, "legacy recipient does notify", attentionRead(t, legacy).Attention, "")
	operatorEqual(t, "DND off recipient does notify", attentionRead(t, loud).Attention, "")
}

func TestAttentionAcceptanceLocalAgentSenderGetsAdvisory(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	receiver := f.peer("quiet", 32)
	sender := f.peer("sender", 32)
	attentionSetting(f, "quiet", "on")
	sender.write(protocol.Message{Type: "msg", MessageID: "local-advisory", To: "quiet", Durable: true, AckRequested: true, Payload: json.RawMessage(`{"text":"ordinary local message"}`)})
	response := attentionRead(t, sender)
	requireOperatorAccepted(t, response.Message, "quiet")
	operatorEqual(t, "local sender told about suppression", attentionAdvisories(t, response.Message)["quiet"], "suppressed")
	operatorEqual(t, "suppression actually enforced", attentionRead(t, receiver).Attention, "suppressed")
}

func TestAttentionAcceptanceUrgentBypassHasNoSuppressionAdvisory(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	receiver := f.peer("quiet", 32)
	attentionSetting(f, "quiet", "on")
	response := attentionCall(f, map[string]any{"type": "operator_send", "message_id": "urgent-advisory", "to": "quiet", "urgent": true, "payload": map[string]string{"text": "urgent local operator message"}})
	requireOperatorAccepted(t, response.Message, "quiet")
	operatorEqual(t, "do not claim suppression for urgent bypass", attentionAdvisories(t, response.Message)["quiet"], "")
	operatorEqual(t, "local urgent bypass delivered", attentionRead(t, receiver).Attention, "urgent")
}
