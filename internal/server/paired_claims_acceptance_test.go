//go:build linux

package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

type pairedClaimFixture struct {
	pair             *instantPair
	sender, receiver instantSide
	asker, worker    *sessionProbe
	localID          string
}

func pairedClaimSetup(t *testing.T) *pairedClaimFixture {
	t.Helper()
	pair := newInstantPair(t)
	sender, receiver := pair.sides()[0], pair.sides()[1]
	asker := &sessionProbe{operatorPeer: sender.f.peer("asker", protocol.Version)}
	worker := &sessionProbe{operatorPeer: receiver.f.peer("worker", protocol.Version)}
	sender.shareRemoteAgents(t, "asker")
	receiver.shareRemoteAgents(t, "worker", "bystander")
	asker.write(protocol.Message{Type: protocol.TypeMsg, Workspace: sender.key, To: "worker", MessageID: "claim-subject", Payload: json.RawMessage(`{"text":"Please investigate this change"}`), Durable: true, AckRequested: true})
	operatorEqual(t, "subject accepted", asker.read().Stage, protocol.StageAccepted)
	got := worker.read()
	requireWorkspaceAgent(t, got, receiver.key, "asker")
	acknowledgeRemoteAgent(t, worker.operatorPeer, got.MessageID)
	return &pairedClaimFixture{pair: pair, sender: sender, receiver: receiver, asker: asker, worker: worker, localID: got.MessageID}
}

func pairedClaimCall(p *sessionProbe, kind, id, task, role string) sessionWire {
	fields := map[string]any{"message_id": id}
	if task != "" {
		fields["task"] = task
	}
	if role != "" {
		fields["role"] = role
	}
	r, _ := p.request(kind, fields)
	return r
}

type pairedClaimRecord struct {
	Agent  string `json:"agent_id"`
	Task   string `json:"task"`
	Role   string `json:"role"`
	State  string `json:"state"`
	Remote *struct {
		Workspace string `json:"workspace"`
	} `json:"remote"`
}

func pairedClaimRecords(t *testing.T, result sessionWire) []pairedClaimRecord {
	t.Helper()
	operatorEqual(t, "claim read succeeded", result.text("type"), protocol.TypeClaimResult)
	var claims []pairedClaimRecord
	if len(result["claims"]) > 0 {
		operatorNoError(t, json.Unmarshal(result["claims"], &claims))
	}
	return claims
}

func (f *pairedClaimFixture) awaitState(t *testing.T, task, state string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r := pairedClaimCall(f.asker, protocol.TypeClaimGet, "claim-subject", "", "")
		for _, claim := range pairedClaimRecords(t, r) {
			if claim.Task != task || claim.State != state {
				continue
			}
			operatorEqual(t, "remote holder", claim.Agent, "worker")
			if claim.Remote == nil {
				t.Fatal("remote claim lacks provenance")
			}
			operatorEqual(t, "remote workspace is canonical", claim.Remote.Workspace, f.sender.key)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sending workspace never learned %s %s", task, state)
}

func TestPairedClaimsAcceptanceLifecycleVisibleToSender(t *testing.T) {
	f := pairedClaimSetup(t)
	for _, step := range []struct{ kind, state string }{{protocol.TypeClaimSet, "active"}, {protocol.TypeClaimRelease, "released"}, {protocol.TypeClaimSet, "active"}, {protocol.TypeClaimDone, "done"}} {
		result := pairedClaimCall(f.worker, step.kind, f.localID, "investigate", "owner")
		operatorEqual(t, "claim mutation accepted", result.text("type"), protocol.TypeClaimResult)
		operatorEqual(t, "shared claim queued for remote visibility", sessionDecode[bool](t, result, "remote_visible"), true)
		for _, claim := range pairedClaimRecords(t, result) {
			if claim.Remote != nil {
				t.Fatal("local holder mislabeled as remote")
			}
		}
		f.awaitState(t, "investigate", step.state)
	}
	// Claim state is coordination: a completed claim never denies a reply.
	f.worker.write(protocol.Message{Type: protocol.TypeMsg, MessageID: "claimed-answer", To: "asker", InReplyTo: f.localID, Durable: true, AckRequested: true, Payload: json.RawMessage(`{"text":"investigation complete"}`)})
	operatorEqual(t, "reply allowed after claim completion", f.worker.read().Stage, protocol.StageAccepted)
	answer := f.asker.read()
	operatorEqual(t, "reply delivered normally", answer.InReplyTo, "claim-subject")
	f.pair.assertOneConnection(t)
}

func TestPairedClaimsAcceptanceUnsharedRecipientKeepsLocalClaim(t *testing.T) {
	f := pairedClaimSetup(t)
	r := f.receiver.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpUnshare, ContactKey: f.receiver.key, ContactAgents: []string{"worker"}, MessageID: "unshare-worker"})
	operatorEqual(t, "unshare accepted", r.Type, protocol.TypeOperatorContactResult)
	result := pairedClaimCall(f.worker, protocol.TypeClaimSet, f.localID, "private-work", "owner")
	claims := pairedClaimRecords(t, result)
	operatorEqual(t, "recipient still owns one local claim", len(claims), 1)
	operatorEqual(t, "local-only visibility", sessionDecode[bool](t, result, "remote_visible"), false)
	operatorEqual(t, "stable visibility reason", result.text("remote_reason"), "not_shared")
	if claims[0].Remote != nil {
		t.Fatal("local claim carries remote authority label")
	}
	remote := pairedClaimCall(f.asker, protocol.TypeClaimGet, "claim-subject", "", "")
	operatorEqual(t, "private claim not leaked", len(pairedClaimRecords(t, remote)), 0)
}

func TestPairedClaimsAcceptanceSharedNonRecipientCannotClaimOrRead(t *testing.T) {
	f := pairedClaimSetup(t)
	bystander := &sessionProbe{operatorPeer: f.receiver.f.peer("bystander", protocol.Version)}
	for _, kind := range []string{protocol.TypeClaimSet, protocol.TypeClaimGet} {
		r := pairedClaimCall(bystander, kind, f.localID, "investigate", "owner")
		operatorEqual(t, "sharing alone is not receipt", r.text("type"), protocol.TypeError)
	}
	result := pairedClaimCall(f.worker, protocol.TypeClaimSet, f.localID, "investigate", "owner")
	operatorEqual(t, "actual recipient can claim after refusal", result.text("type"), protocol.TypeClaimResult)
	f.awaitState(t, "investigate", "active")
}

func TestPairedClaimsAcceptanceSelectorsAreExclusive(t *testing.T) {
	f := pairedClaimSetup(t)
	for _, kind := range []string{protocol.TypeClaimSet, protocol.TypeClaimRelease, protocol.TypeClaimDone, protocol.TypeClaimGet} {
		r, _ := f.worker.request(kind, map[string]any{"message_id": f.localID, "operator_message_id": "invented-operator", "task": "investigate"})
		operatorEqual(t, "mixed selector rejected", r.text("type"), protocol.TypeError)
	}
	r := pairedClaimCall(f.worker, protocol.TypeClaimGet, f.localID, "", "")
	operatorEqual(t, "invalid requests left no claims", len(pairedClaimRecords(t, r)), 0)
}
