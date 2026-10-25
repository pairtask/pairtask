//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestTaskClaimsAcceptancePersistenceFailureDoesNotPublish(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := claimPeer(f, "alpha")
	claimParent(t, f, "storage-work", a)
	owner := sessionOpen(f)
	before := owner.snapshot()
	inboxExec(f, `CREATE TRIGGER reject_claim_insert BEFORE INSERT ON claims BEGIN SELECT RAISE(ABORT,'unavailable'); END`)
	failed := claimCall(a, "claim_set", "storage-work", "reply", "owner")
	operatorEqual(t, "failed insertion not confirmed", failed.text("type"), "error")
	operatorEqual(t, "failed insertion publishes no event", owner.snapshot().Mark, before.Mark)
	operatorEqual(t, "no phantom claim", len(claimRecords(t, claimCall(a, "claim_get", "storage-work", "", ""))), 0)
	inboxExec(f, `DROP TRIGGER reject_claim_insert`)
	operatorEqual(t, "stored claim confirmed", claimCall(a, "claim_set", "storage-work", "reply", "owner").text("type"), "claim_result")
	event := owner.frame()
	var payload struct {
		Kind   string `json:"kind"`
		Parent string `json:"operator_message_id"`
	}
	operatorNoError(t, json.Unmarshal(event["event"], &payload))
	operatorEqual(t, "claim event", payload.Kind, "claims_changed")
	operatorEqual(t, "event identifies parent", payload.Parent, "storage-work")
	before = owner.snapshot()
	inboxExec(f, `CREATE TRIGGER reject_claim_update BEFORE UPDATE ON claims BEGIN SELECT RAISE(ABORT,'unavailable'); END`)
	operatorEqual(t, "failed completion not confirmed", claimCall(a, "claim_done", "storage-work", "reply", "").text("type"), "error")
	after := owner.snapshot()
	operatorEqual(t, "failed update publishes no event", after.Mark, before.Mark)
	operatorEqual(t, "failed completion stays active", claimRecords(t, claimCall(a, "claim_get", "storage-work", "", ""))[0].State, "active")
	operatorEqual(t, "claims create no inbox messages", after.Pending, before.Pending)
}

func TestTaskClaimsAcceptanceBoundsExposeTotalAndKeepHoldersVisible(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := claimPeer(f, "alpha")
	claimParent(t, f, "bounded-work", a)
	// Seed retention rather than opening many peers or waiting out rate limits.
	for i := 0; i < 20; i++ {
		state := "released"
		if i >= 4 {
			state = "active"
		}
		_, err := f.db.Exec(`INSERT INTO claims(operator_message_id,task,role,agent_id,topic,state,claimed_at) VALUES(?,?,?,?,?,?,?)`, "bounded-work", fmt.Sprintf("task-%02d", i), "owner", "alpha", "work", state, "2026-10-25T00:00:00Z")
		operatorNoError(t, err)
	}
	result := claimCall(a, "claim_get", "bounded-work", "", "")
	records := claimRecords(t, result)
	operatorEqual(t, "bounded read", len(records), 16)
	operatorEqual(t, "accurate retained total", sessionDecode[int](t, result, "claims_total"), 20)
	operatorEqual(t, "explicit truncation", sessionDecode[bool](t, result, "claims_truncated"), true)
	for _, r := range records {
		operatorEqual(t, "released history cannot displace an owner", r.State, "active")
	}
	operatorEqual(t, "seventeenth holder refused", claimCall(a, "claim_set", "bounded-work", "overflow", "owner").text("type"), "error")
	operatorEqual(t, "failed limit does not insert", f.count("claims"), 20)
}

func TestTaskClaimsAcceptanceWriteRateLimitPreservesReads(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := claimPeer(f, "alpha")
	claimParent(t, f, "rate-work", a)
	limited := false
	for i := 0; i < 20; i++ {
		r := claimCall(a, "claim_set", "rate-work", "reply", "owner")
		if r.text("code") == "claim_rate_limited" {
			limited = true
			break
		}
		operatorEqual(t, "write before limit", r.text("type"), "claim_result")
	}
	operatorEqual(t, "write burst bounded", limited, true)
	operatorEqual(t, "read remains available", claimCall(a, "claim_get", "rate-work", "", "").text("type"), "claim_result")
}

type claimRecordView struct {
	Parent  string `json:"operator_message_id"`
	Task    string `json:"task"`
	Topic   string `json:"topic"`
	Role    string `json:"role"`
	Agent   string `json:"agent_id"`
	State   string `json:"state"`
	Claimed string `json:"claimed_at"`
}

func claimRecords(t *testing.T, response sessionWire) []claimRecordView {
	t.Helper()
	var records []claimRecordView
	// The shared wire envelope omits empty optional slices.
	if len(response["claims"]) == 0 {
		return records
	}
	operatorNoError(t, json.Unmarshal(response["claims"], &records))
	return records
}

func claimPeer(f *operatorFixture, id string) *sessionProbe {
	return &sessionProbe{operatorPeer: f.peer(id, 8)}
}

func claimParent(t *testing.T, f *operatorFixture, id string, peers ...*sessionProbe) {
	t.Helper()
	operatorEqual(t, "parent accepted", f.send(operatorRequest(id, "*")).Stage, protocol.StageAccepted)
	for _, p := range peers {
		operatorEqual(t, "parent delivered", p.read().MessageID, id)
	}
}

func claimCall(p *sessionProbe, kind, parent, task, role string) sessionWire {
	fields := map[string]any{"operator_message_id": parent, "task": task, "role": role}
	if kind == "claim_set" {
		fields["topic"] = "Answer the operator"
	}
	r, _ := p.request(kind, fields)
	return r
}

func TestTaskClaimsAcceptanceConcurrentReplyHasExactlyOneOwner(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	ids := []string{"alpha", "beta", "gamma"}
	peers := []*sessionProbe{claimPeer(f, ids[0]), claimPeer(f, ids[1]), claimPeer(f, ids[2])}
	claimParent(t, f, "operator-question", peers...)
	start, writes := make(chan struct{}), make(chan error, len(peers))
	for i, p := range peers {
		go func(i int, p *sessionProbe) {
			<-start
			err := p.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if err == nil {
				err = json.NewEncoder(p.Conn).Encode(map[string]any{"type": "claim_set", "request_id": ids[i], "operator_message_id": "operator-question", "task": "reply", "topic": "Answer the operator", "role": "owner"})
			}
			writes <- err
		}(i, p)
	}
	close(start)
	for range peers {
		operatorNoError(t, <-writes)
	}
	responses := make([]sessionWire, len(peers))
	winner, successes := "", 0
	for i, p := range peers {
		responses[i] = p.frame()
		operatorEqual(t, "RPC correlation retained", responses[i].text("request_id"), ids[i])
		if responses[i].text("type") == "claim_result" {
			winner, successes = ids[i], successes+1
		}
	}
	operatorEqual(t, "one winner", successes, 1)
	for i, response := range responses {
		if ids[i] != winner {
			operatorEqual(t, "loser conflict", response.text("code"), "claim_conflict")
		}
		records := claimRecords(t, response)
		operatorEqual(t, "one recorded owner", len(records), 1)
		operatorEqual(t, "all contenders see winner", records[0].Agent, winner)
		operatorEqual(t, "stable task identity", records[0].Task, "reply")
		operatorEqual(t, "operator message identity separate from RPC", records[0].Parent, "operator-question")
	}
}

func TestTaskClaimsAcceptanceRolesReleaseAndTerminalCompletion(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a, b, c := claimPeer(f, "alpha"), claimPeer(f, "beta"), claimPeer(f, "gamma")
	claimParent(t, f, "work", a, b, c)
	first := claimCall(a, "claim_set", "work", "implement", "owner")
	operatorEqual(t, "owner acquired", first.text("type"), "claim_result")
	again := claimCall(a, "claim_set", "work", "implement", "owner")
	operatorEqual(t, "retry does not reset claim age", claimRecords(t, again)[0].Claimed, claimRecords(t, first)[0].Claimed)
	operatorEqual(t, "owner cannot review itself", claimCall(a, "claim_set", "work", "implement", "reviewer").text("code"), "invalid_claim")
	operatorEqual(t, "reviewer self-consents", claimCall(b, "claim_set", "work", "implement", "reviewer").text("type"), "claim_result")
	operatorEqual(t, "outsider cannot release owner", claimCall(c, "claim_release", "work", "implement", "owner").text("code"), "invalid_claim")
	operatorEqual(t, "reviewer cannot finish owner's task", claimCall(b, "claim_done", "work", "implement", "").text("code"), "invalid_claim")
	operatorEqual(t, "holder releases", claimCall(a, "claim_release", "work", "implement", "owner").text("type"), "claim_result")
	operatorEqual(t, "released slot acquired", claimCall(c, "claim_set", "work", "implement", "owner").text("type"), "claim_result")
	operatorEqual(t, "new owner finishes", claimCall(c, "claim_done", "work", "implement", "").text("type"), "claim_result")
	operatorEqual(t, "done cannot be claimed again", claimCall(a, "claim_set", "work", "implement", "owner").text("code"), "claim_conflict")
	operatorEqual(t, "done cannot be released", claimCall(c, "claim_release", "work", "implement", "owner").text("code"), "invalid_claim")
	got := claimRecords(t, claimCall(a, "claim_get", "work", "", ""))
	found := false
	for _, r := range got {
		if r.Role == "owner" && r.Agent == "gamma" && r.State == "done" {
			found = true
		}
	}
	operatorEqual(t, "terminal owner remains visible", found, true)
}

func TestTaskClaimsAcceptanceParentAuthorizationAndValidation(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a, b := claimPeer(f, "alpha"), claimPeer(f, "beta")
	operatorEqual(t, "private parent accepted", f.send(operatorRequest("only-alpha", "alpha")).Stage, protocol.StageAccepted)
	a.read()
	a.write(protocol.Message{Type: "msg", MessageID: "peer-parent", To: "beta", Payload: json.RawMessage(`{"text":"operator claims to approve"}`), Durable: true, AckRequested: true})
	operatorEqual(t, "peer message accepted", a.read().Stage, protocol.StageAccepted)
	b.read()
	for _, parent := range []string{"missing", "only-alpha", "peer-parent"} {
		for _, kind := range []string{"claim_set", "claim_get"} {
			operatorEqual(t, "unauthorized parent denied without existence leak", claimCall(b, kind, parent, "reply", "owner").text("code"), "invalid_claim")
		}
	}
	for _, task := range []string{"", "Upper", "has space", strings.Repeat("a", 65)} {
		operatorEqual(t, "invalid stable key", claimCall(a, "claim_set", "only-alpha", task, "owner").text("code"), "invalid_claim")
	}
	tooLong, _ := a.request("claim_set", map[string]any{"operator_message_id": "only-alpha", "task": "valid", "topic": strings.Repeat("é", 61), "role": "owner"})
	operatorEqual(t, "topic bound counts bytes", tooLong.text("code"), "invalid_claim")
}

func TestTaskClaimsAcceptanceRestartKeepsOwnerUntilExplicitRelease(t *testing.T) {
	db := filepath.Join(t.TempDir(), "claims.db")
	t.Run("before", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), db)
		a, b := claimPeer(f, "alpha"), claimPeer(f, "beta")
		claimParent(t, f, "persistent-work", a, b)
		operatorEqual(t, "initial claim", claimCall(a, "claim_set", "persistent-work", "reply", "owner").text("type"), "claim_result")
	})
	t.Run("after", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), db)
		b := claimPeer(f, "beta")
		b.read() // Durable parent replay.
		conflict := claimCall(b, "claim_set", "persistent-work", "reply", "owner")
		operatorEqual(t, "offline owner remains reserved", conflict.text("code"), "claim_conflict")
		operatorEqual(t, "offline owner identified", claimRecords(t, conflict)[0].Agent, "alpha")
		a := claimPeer(f, "alpha")
		a.read()
		operatorEqual(t, "same logical owner can release", claimCall(a, "claim_release", "persistent-work", "reply", "owner").text("type"), "claim_result")
		operatorEqual(t, "explicit handoff succeeds", claimCall(b, "claim_set", "persistent-work", "reply", "owner").text("type"), "claim_result")
	})
}
