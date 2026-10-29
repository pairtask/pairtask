//go:build linux

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

// Lifecycle calls may drain the initial assignment, but never duplicate notices
// or notifications about later state changes on the same connection.
func reviewNoticeEvent(p *sessionProbe, event sessionWire) {
	p.t.Helper()
	var msg protocol.Message
	operatorNoError(p.t, json.Unmarshal([]byte(sessionJSON(event)), &msg))
	operatorEqual(p.t, "assignment identifies review", msg.ReviewID != "", true)
	reviewNoticeCheck(p.t, msg, msg.ReviewID)
	if p.reviewNotices == nil {
		p.reviewNotices = make(map[string]bool)
	}
	operatorEqual(p.t, "one assignment per connection", p.reviewNotices[msg.ReviewID], false)
	p.reviewNotices[msg.ReviewID] = true
}

func reviewNoticeCheck(t *testing.T, msg protocol.Message, id string) {
	t.Helper()
	noticeID := "review:" + id
	if len(noticeID) > 128 {
		sum := sha256.Sum256([]byte(id))
		noticeID = "review:" + hex.EncodeToString(sum[:])
	}
	requireOperatorDelivery(t, msg, noticeID)
	operatorEqual(t, "review identity", msg.ReviewID, id)
	operatorEqual(t, "assigned recipient", msg.To, "alpha")
	var body struct {
		Text string `json:"text"`
	}
	operatorNoError(t, json.Unmarshal(msg.Payload, &body))
	for _, part := range []string{id, "github.com/team/project#201", "accept_review", "decline_review"} {
		operatorEqual(t, "assignment includes "+part, strings.Contains(body.Text, part), true)
	}
}

func TestReviewNotifyAcceptanceFullInboxRejectsNewButAllowsRetry(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	reviewTestView(t, reviewTestRequest(f, "existing", reviewTestSpec()), "operator_review_result")
	reviewNoticeCheck(t, p.read(), "existing")
	// Fill accounting without creating hundreds of unrelated network requests.
	_, err := f.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x < ?)
INSERT INTO durable_messages(message_id,wire_bytes,storage_bytes) SELECT 'quota-'||x,1,1 FROM n`, store.MaxPendingMessages-1)
	operatorNoError(t, err)
	_, err = f.db.Exec(`INSERT INTO durable_inbox(message_id,agent_id,session_id)
SELECT message_id,'alpha','' FROM durable_messages WHERE message_id LIKE 'quota-%'`)
	operatorNoError(t, err)
	operatorEqual(t, "new request refused at inbox bound", reviewTestRequest(f, "overflow", reviewTestSpec()).text("code"), "inbox_full")
	reviewTestView(t, reviewTestRequest(f, "existing", reviewTestSpec()), "operator_review_result")
	operatorEqual(t, "overflow leaves original review only", f.count("reviews"), 1)
	operatorEqual(t, "overflow leaves original message only", f.count("messages"), 1)
	_, events := p.request("review_get", nil)
	operatorEqual(t, "neither failed admission nor retry pushes", len(events), 0)
}

func TestReviewNotifyAcceptanceMessageCollisionRollsBack(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	requireOperatorAccepted(t, f.send(operatorRequest("review:collision", "alpha")), "alpha")
	p.read()
	operatorEqual(t, "notification ID collision refused", reviewTestRequest(f, "collision", reviewTestSpec()).text("code"), "duplicate_message")
	operatorEqual(t, "collision creates no review", f.count("reviews"), 0)
	operatorEqual(t, "collision preserves original message", f.count("messages"), 1)
	_, events := p.request("review_get", nil)
	operatorEqual(t, "collision never pushes", len(events), 0)
}

func TestReviewNotifyAcceptanceLongestReviewIDReplays(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	id := strings.Repeat("r", 128)
	reviewTestView(t, reviewTestRequest(f, id, reviewTestSpec()), "operator_review_result")
	first := p.read()
	reviewNoticeCheck(t, first, id)
	roomDisconnect(f, p, "alpha")
	p = reviewTestPeer(f, "alpha")
	msg := p.read()
	reviewNoticeCheck(t, msg, id)
	operatorEqual(t, "replay retains notification identity", msg.MessageID, first.MessageID)
	operatorEqual(t, "replay retains sequence", msg.Seq, first.Seq)
}

func TestReviewNotifyAcceptancePushRetryAndStages(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a, b := reviewTestPeer(f, "alpha"), reviewTestPeer(f, "beta")
	reviewTestView(t, reviewTestRequest(f, "pushed", reviewTestSpec()), "operator_review_result")
	// Read without an agent request: this must be an unsolicited push.
	msg := a.read()
	reviewNoticeCheck(t, msg, "pushed")
	operatorEqual(t, "initial push", msg.Replayed, false)
	reviewTestView(t, reviewTestRequest(f, "pushed", reviewTestSpec()), "operator_review_result")
	for _, p := range []*sessionProbe{a, b} {
		_, events := p.request("review_get", nil)
		operatorEqual(t, "no duplicate or other-agent notification", len(events), 0)
	}
	owner := sessionOpen(f)
	operatorEqual(t, "operator sees acceptance", owner.stages(msg.MessageID, "", 1).Recipients[0].Stage, "accepted")
	a.write(protocol.Message{Type: "ack", MessageID: msg.MessageID, Stage: "adapter_received"})
	sessionAgentBarrier(t, a.operatorPeer)
	operatorEqual(t, "operator sees receipt", owner.stages(msg.MessageID, "", 1).Recipients[0].Stage, "adapter_received")
	roomAcknowledge(a, msg.MessageID)
	operatorEqual(t, "operator sees acknowledgment", owner.stages(msg.MessageID, "", 1).Recipients[0].Stage, "agent_acknowledged")
	wire, _ := owner.request("operator_history", historyQuery(0, 0, 16))
	history := sessionDecode[historyPage](t, wire, "history")
	operatorEqual(t, "notification in operator history", len(history.Entries), 1)
	reviewNoticeCheck(t, history.Entries[0].Message, "pushed")
	operatorEqual(t, "one durable notification", f.count("messages"), 1)
}

func TestReviewNotifyAcceptanceOfflineRestartReplayAndAck(t *testing.T) {
	database := filepath.Join(t.TempDir(), "review-notify.db")
	t.Run("register", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		reviewTestPeer(f, "alpha")
	})
	t.Run("assign-offline", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		reviewTestView(t, reviewTestRequest(f, "offline", reviewTestSpec()), "operator_review_result")
	})
	t.Run("replay", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := reviewTestPeer(f, "alpha")
		msg := p.read()
		reviewNoticeCheck(t, msg, "offline")
		operatorEqual(t, "replayed after restart", msg.Replayed, true)
		roomAcknowledge(p, msg.MessageID)
	})
	if t.Failed() {
		return
	}
	t.Run("ack-survives-restart", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), database)
		p := reviewTestPeer(f, "alpha")
		reviewTestView(t, reviewTestRequest(f, "offline", reviewTestSpec()), "operator_review_result")
		_, events := p.request("review_get", nil)
		operatorEqual(t, "acknowledged assignment never requeued", len(events), 0)
		operatorEqual(t, "one notification across restarts", f.count("messages"), 1)
	})
}

func TestReviewNotifyAcceptanceIneligibleReviewerIsAtomic(t *testing.T) {
	for _, version := range []int{0, 3} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			if version != 0 {
				f.peer("alpha", version)
			}
			operatorEqual(t, "ineligible reviewer refused", reviewTestRequest(f, "ineligible", reviewTestSpec()).text("type"), "error")
			for _, table := range []string{"reviews", "review_events", "messages", "durable_inbox"} {
				operatorEqual(t, "no partial "+table, f.count(table), 0)
			}
		})
	}
}

func TestReviewNotifyAcceptanceStorageFailureRollsBack(t *testing.T) {
	for _, table := range []string{"messages", "review_events"} {
		t.Run(table, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			p := reviewTestPeer(f, "alpha")
			_, err := f.db.Exec("CREATE TRIGGER refuse_notice BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT,'test failure'); END")
			operatorNoError(t, err)
			operatorEqual(t, "storage failure returned", reviewTestRequest(f, "failed", reviewTestSpec()).text("code"), "internal")
			for _, name := range []string{"reviews", "review_events", "messages", "message_metadata", "durable_inbox"} {
				operatorEqual(t, "rolled back "+name, f.count(name), 0)
			}
			_, events := p.request("review_get", nil)
			operatorEqual(t, "failed transaction never pushes", len(events), 0)
		})
	}
}
