//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type statusSnapshotView struct {
	Watermark protocol.Watermark `json:"watermark"`
	Statuses  []struct {
		AgentID   string `json:"agent_id"`
		State     string `json:"state"`
		Task      string `json:"task"`
		Connected bool   `json:"connected"`
	} `json:"agent_status"`
	Total        int    `json:"agent_status_total"`
	Truncated    bool   `json:"agent_status_truncated"`
	InboxHead    uint64 `json:"inbox_head_seq"`
	InboxPending int    `json:"inbox_pending"`
}

func statusSnapshot(t *testing.T, p *sessionProbe) statusSnapshotView {
	t.Helper()
	r, _ := p.request("operator_snapshot", nil)
	operatorEqual(t, "snapshot response", r.text("type"), "operator_snapshot")
	var out statusSnapshotView
	operatorNoError(t, json.Unmarshal(r["snapshot"], &out))
	return out
}

func statusDeclare(t *testing.T, p *sessionProbe, state string) sessionWire {
	t.Helper()
	r, _ := p.request("agent_status_set", map[string]any{"agent_status": map[string]any{"state": state, "task": "#133", "step": "test"}})
	return r
}

func TestAgentStatusOperatorEventAndSnapshotDoNotCreateMessages(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	agent := &sessionProbe{operatorPeer: f.peer("worker", protocol.Version)}
	owner := sessionOpen(f)
	before := statusSnapshot(t, owner)
	operatorEqual(t, "initial statuses", len(before.Statuses), 0)
	response := statusDeclare(t, agent, "active")
	operatorEqual(t, "write confirmation", response.text("type"), "agent_status_result")
	eventFrame := owner.frame()
	var event protocol.SessionEvent
	operatorNoError(t, json.Unmarshal(eventFrame["event"], &event))
	operatorEqual(t, "notification", event.Kind, "status_changed")
	operatorEqual(t, "subject", event.AgentID, "worker")
	operatorEqual(t, "same epoch", event.Watermark.Epoch, before.Watermark.Epoch)
	operatorEqual(t, "new revision", event.Watermark.Revision > before.Watermark.Revision, true)
	after := statusSnapshot(t, owner)
	operatorEqual(t, "one current status", len(after.Statuses), 1)
	operatorEqual(t, "status total", after.Total, 1)
	operatorEqual(t, "complete status list", after.Truncated, false)
	operatorEqual(t, "status identity", after.Statuses[0].AgentID, "worker")
	operatorEqual(t, "status task", after.Statuses[0].Task, "#133")
	operatorEqual(t, "status state", after.Statuses[0].State, "active")
	operatorEqual(t, "status connected", after.Statuses[0].Connected, true)
	operatorEqual(t, "no inbox sequence", after.InboxHead, before.InboxHead)
	operatorEqual(t, "no unread status messages", after.InboxPending, before.InboxPending)
	for _, table := range []string{"messages", "message_metadata", "message_receipts", "durable_messages", "durable_inbox"} {
		operatorEqual(t, "empty "+table, f.count(table), 0)
	}
	// Reading and refreshing a snapshot does not acknowledge or mutate status.
	again := statusSnapshot(t, owner)
	operatorEqual(t, "read does not advance watermark", again.Watermark, after.Watermark)
}

func TestAgentStatusOperatorRetainsDisconnectedDeclaration(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	agent := &sessionProbe{operatorPeer: f.peer("worker", protocol.Version)}
	operatorEqual(t, "declared", statusDeclare(t, agent, "blocked").text("type"), "agent_status_result")
	owner := sessionOpen(f)
	statusSnapshot(t, owner)
	agent.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot := statusSnapshot(t, owner)
		if len(snapshot.Statuses) == 1 && !snapshot.Statuses[0].Connected {
			operatorEqual(t, "historical state preserved", snapshot.Statuses[0].State, "blocked")
			operatorEqual(t, "historical task preserved", snapshot.Statuses[0].Task, "#133")
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("disconnected status lost or still current: %+v", snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAgentStatusPersistenceFailureCannotConfirmOrPublish(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	agent := &sessionProbe{operatorPeer: f.peer("worker", protocol.Version)}
	operatorEqual(t, "initial declaration", statusDeclare(t, agent, "active").text("type"), "agent_status_result")
	owner := sessionOpen(f)
	before := statusSnapshot(t, owner)
	inboxExec(f, `CREATE TRIGGER status_insert_failure BEFORE INSERT ON agent_status BEGIN SELECT RAISE(ABORT,'unavailable'); END`)
	inboxExec(f, `CREATE TRIGGER status_update_failure BEFORE UPDATE ON agent_status BEGIN SELECT RAISE(ABORT,'unavailable'); END`)
	response := statusDeclare(t, agent, "done")
	operatorEqual(t, "failed storage not confirmed", response.text("type"), "error")
	after := statusSnapshot(t, owner)
	operatorEqual(t, "failed storage no state event", after.Watermark, before.Watermark)
	operatorEqual(t, "old status preserved", len(after.Statuses), 1)
	operatorEqual(t, "failed done not displayed", after.Statuses[0].State, "active")
}

func TestAgentStatusOperatorSnapshotBoundAndConnectedPriority(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	for i := 0; i < 65; i++ {
		agent := &sessionProbe{operatorPeer: f.peer(fmt.Sprintf("offline-%02d", i), protocol.Version)}
		operatorEqual(t, "retained declaration", statusDeclare(t, agent, "idle").text("type"), "agent_status_result")
		agent.Close()
	}
	active := &sessionProbe{operatorPeer: f.peer("zzz-current", protocol.Version)}
	operatorEqual(t, "current declaration", statusDeclare(t, active, "active").text("type"), "agent_status_result")
	owner := sessionOpen(f)
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := statusSnapshot(t, owner)
		operatorEqual(t, "bounded status list", len(got.Statuses), 64)
		operatorEqual(t, "full status count", got.Total, 66)
		operatorEqual(t, "omission explicit", got.Truncated, true)
		if got.Statuses[0].AgentID == "zzz-current" {
			operatorEqual(t, "current first", got.Statuses[0].Connected, true)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("offline records displaced current agent: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAgentStatusOperatorLargeRetentionHasAccurateTotal(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	// Populate historical records directly to exercise the read bound without
	// making this regression depend on a thousand connection lifecycles.
	inboxExec(f, `WITH RECURSIVE ids(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM ids WHERE n<1100)
INSERT INTO agent_status(agent_id,session_id,state,task,step,waiting_on,ref,declared_at)
SELECT printf('retained-%04d',n),'old-session','idle','','','','','2026-01-01T00:00:00Z' FROM ids`)
	// Reconnected agents still have historical declarations. Those records
	// must not displace a declaration from an actually current session.
	for i := 1; i <= 65; i++ {
		f.peer(fmt.Sprintf("retained-%04d", i), protocol.Version)
	}
	agent := &sessionProbe{operatorPeer: f.peer("zzz-current", protocol.Version)}
	operatorEqual(t, "live declaration", statusDeclare(t, agent, "active").text("type"), "agent_status_result")
	owner := sessionOpen(f)
	got := statusSnapshot(t, owner)
	operatorEqual(t, "true retention total", got.Total, 1101)
	operatorEqual(t, "bounded page", len(got.Statuses), 64)
	operatorEqual(t, "retention truncation visible", got.Truncated, true)
	operatorEqual(t, "live agent beyond historical prefix included first", got.Statuses[0].AgentID, "zzz-current")
}
