//go:build linux

package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func filesServerSample() []map[string]any {
	return []map[string]any{{"path": "/unavailable/opaque-reference.bin", "size": float64(123), "sha256": strings.Repeat("b", 64)}}
}

func filesServerEqual(t *testing.T, object any, want []map[string]any) {
	t.Helper()
	data, err := json.Marshal(object)
	operatorNoError(t, err)
	var out struct {
		Files []map[string]any `json:"files"`
	}
	operatorNoError(t, json.Unmarshal(data, &out))
	if !reflect.DeepEqual(out.Files, want) {
		t.Fatalf("file references lost or rewritten: got%v want%v", out.Files, want)
	}
}

func TestFileRefsServerAcceptanceTwoBrokersDeliverMetadataAndRetainHistory(t *testing.T) {
	pair := newInstantPair(t)
	sender, receiver := pair.sides()[0], pair.sides()[1]
	asker := &sessionProbe{operatorPeer: sender.f.peer("asker", protocol.Version)}
	worker := receiver.f.peer("worker", protocol.Version)
	sender.shareRemoteAgents(t, "asker")
	receiver.shareRemoteAgents(t, "worker")
	files := filesServerSample()
	asker.send(map[string]any{"type": protocol.TypeMsg, "message_id": "file-handoff", "to": "worker", "workspace": sender.key, "durable": true, "ack_requested": true, "files": files, "payload": map[string]any{"text": "Metadata supplied by caller; file obtained separately"}})
	operatorEqual(t, "metadata send accepted", asker.read().Stage, protocol.StageAccepted)
	message := worker.read()
	requireWorkspaceAgent(t, message, receiver.key, "asker")
	var payload map[string]any
	operatorNoError(t, json.Unmarshal(message.Payload, &payload))
	filesServerEqual(t, payload, files)
	for _, side := range pair.sides() {
		row := side.awaitRemoteView(t, func(m protocol.ContactMessageView) bool { return m.ID == "file-handoff" })
		filesServerEqual(t, row, files)
	}
	pair.assertOneConnection(t)
}
