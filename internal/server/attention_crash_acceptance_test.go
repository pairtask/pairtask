//go:build linux

package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAttentionAcceptanceCrashDoesNotRetainAdapterCapability(t *testing.T) {
	before := newOperatorFixture(t, os.Getuid(), "")
	before.peer("worker", 31)
	state := attentionSetting(before, "worker", "on")
	operatorEqual(t, "old live adapter cannot enforce", state.State.Enforced, false)
	// Snapshot while the old adapter is connected: its disconnect cleanup
	// must not run against the database that the restarted broker opens.
	database := filepath.Join(t.TempDir(), "crash-snapshot.db")
	_, err := before.db.Exec("VACUUM INTO ?", database)
	operatorNoError(t, err)
	after := newOperatorFixture(t, os.Getuid(), database)
	state = attentionSetting(after, "worker", "status")
	operatorEqual(t, "saved DND survives the crash", state.State.DND, true)
	operatorEqual(t, "offline agent is not currently enforcing", state.State.Enforced, false)
	requireOperatorAccepted(t, after.send(operatorRequest("admitted-after-crash", "worker")), "worker")
	receiver := after.peer("worker", 32)
	message := attentionRead(t, receiver)
	operatorEqual(t, "offline admission survives", message.MessageID, "admitted-after-crash")
	operatorEqual(t, "offline admission uses saved DND, not a dead adapter", message.Attention, "suppressed")
}
