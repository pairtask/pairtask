//go:build linux

package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestAttentionAcceptanceEnforcementFollowsCurrentAdapter(t *testing.T) {
	database := filepath.Join(t.TempDir(), "attention-capability.db")
	if !t.Run("enable with capable adapter", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		f.peer("worker", 32)
		state := attentionSetting(f, "worker", "on")
		operatorEqual(t, "current capable adapter enforces", state.State.Enforced, true)
	}) {
		return
	}
	if !t.Run("offline is not enforcement", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		state := attentionSetting(f, "worker", "status")
		operatorEqual(t, "stored preference survives", state.State.DND, true)
		operatorEqual(t, "past capability is not current enforcement", state.State.Enforced, false)
		if state.State.Reason == "" {
			t.Fatal("offline uncertainty needs a reason")
		}
	}) {
		return
	}
	if !t.Run("downgraded adapter reports unsupported", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		receiver := f.peer("worker", 31)
		state := attentionSetting(f, "worker", "status")
		operatorEqual(t, "DND setting kept through downgrade", state.State.DND, true)
		operatorEqual(t, "legacy adapter cannot enforce saved preference", state.State.Enforced, false)
		if state.State.Reason == "" {
			t.Fatal("unsupported adapter needs a reason")
		}
		requireOperatorAccepted(t, f.send(operatorRequest("legacy-after-restart", "worker")), "worker")
		message := attentionRead(t, receiver)
		operatorEqual(t, "legacy delivery does notify", message.Attention, "")
		receiver.write(protocol.Message{Type: "ack", MessageID: message.MessageID, Stage: "adapter_received"})
		receiver.write(protocol.Message{Type: "ack", MessageID: message.MessageID, Stage: "agent_acknowledged"})
		operatorEqual(t, "explicit old-adapter acknowledgment", attentionRead(t, receiver).Stage, "agent_acknowledged")
	}) {
		return
	}
	t.Run("upgraded adapter enforces without resetting preference", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		receiver := f.peer("worker", 32)
		state := attentionSetting(f, "worker", "status")
		operatorEqual(t, "preference stays on", state.State.DND, true)
		operatorEqual(t, "current capability used", state.State.Enforced, true)
		attentionQuiet(f, receiver, "quiet-after-upgrade")
	})
}
