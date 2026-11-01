//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

type assignmentAcceptanceRecord struct {
	Parent   string  `json:"operator_message_id"`
	Agent    string  `json:"agent_id"`
	Task     string  `json:"task"`
	State    string  `json:"state"`
	Assigned string  `json:"assigned_at"`
	Finished *string `json:"finished_at"`
}

func assignmentPage(t *testing.T, frame sessionWire) []assignmentAcceptanceRecord {
	t.Helper()
	for _, field := range []string{"assignments", "assignments_total", "assignments_truncated"} {
		if _, ok := frame[field]; !ok {
			t.Fatalf("typed assignment page missing %s: %s", field, sessionJSON(frame))
		}
	}
	return sessionDecode[[]assignmentAcceptanceRecord](t, frame, "assignments")
}

func assignmentState(t *testing.T, frame sessionWire, agent, expected string) assignmentAcceptanceRecord {
	t.Helper()
	for _, record := range assignmentPage(t, frame) {
		if record.Agent == agent {
			operatorEqual(t, "canonical assignment task", record.Task, "reply")
			operatorEqual(t, "assignment state", record.State, expected)
			if record.Assigned == "" {
				t.Fatal("assignment has no broker timestamp")
			}
			return record
		}
	}
	t.Fatalf("missing assignment for %s: %s", agent, sessionJSON(frame))
	return assignmentAcceptanceRecord{}
}

func assignmentCounts(f *operatorFixture) map[string]int {
	counts := map[string]int{}
	for _, table := range []string{"messages", "operator_requests", "durable_messages", "durable_inbox", "message_receipts", "assignments", "claims"} {
		counts[table] = f.count(table)
	}
	return counts
}

func TestAssignmentsAcceptanceInvalidAdmissionHasNoPersistentSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		targets []string
		noPeers bool
		square  bool
	}{
		{name: "unknown", targets: []string{"missing-one"}},
		{name: "offline-member", targets: []string{"alpha-offline"}},
		{name: "wrong-room", targets: []string{"beta-one"}},
		{name: "old-version", targets: []string{"alpha-two"}},
		{name: "multiple-invalid", targets: []string{"alpha-offline", "alpha-two", "beta-one"}},
		{name: "no-peers-room", targets: []string{"alpha-one", "alpha-offline"}, noPeers: true},
		{name: "no-peers-square", targets: []string{"alpha-offline", "missing-one"}, noPeers: true, square: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := roomFixture(t)
			if !tc.noPeers {
				roomPeer(f, "alpha-one", 16)
				roomPeer(f, "alpha-two", 15)
				roomPeer(f, "beta-one", 16)
			}
			before := assignmentCounts(f)
			tags := slices.Clone(tc.targets)
			if !tc.noPeers {
				tags = append(tags, "alpha-one")
			}
			request := mentionedRoomMessage("refused-"+tc.name, tags...)
			if tc.square {
				delete(request, "room")
			}
			response := roomOperator(f, request)
			operatorEqual(t, "entire send refused", response.text("type"), "error")
			operatorEqual(t, "tag admission refusal", response.text("code"), "invalid_mention")
			for _, target := range tc.targets {
				if !strings.Contains(response.text("detail"), target) {
					t.Fatalf("bad tag %s not named: %s", target, sessionJSON(response))
				}
			}
			for table, count := range before {
				operatorEqual(t, "rejection leaves "+table+" unchanged", f.count(table), count)
			}
			var auditCount int
			operatorNoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM operator_audit WHERE message_id=? AND result='rejected' AND detail='invalid_mention'`, "refused-"+tc.name).Scan(&auditCount))
			operatorEqual(t, "one completed rejection audit retained", auditCount, 1)
			operatorNoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM operator_audit WHERE message_id=? AND result='pending'`, "refused-"+tc.name).Scan(&auditCount))
			operatorEqual(t, "rejection leaves no pending audit", auditCount, 0)
		})
	}
}

func TestAssignmentsAcceptanceUntaggedEmptyBroadcastRetainsUnavailable(t *testing.T) {
	for _, room := range []string{"", "alpha"} {
		t.Run("room="+room, func(t *testing.T) {
			f := roomFixture(t)
			before := assignmentCounts(f)
			request := roomMessage("untagged-empty", "*")
			request["type"] = "operator_send"
			if room != "" {
				request["room"] = room
			}
			operatorEqual(t, "untagged empty audience keeps its error", roomOperator(f, request).text("code"), "recipient_unavailable")
			for table, count := range before {
				operatorEqual(t, "empty audience leaves "+table+" unchanged", f.count(table), count)
			}
		})
	}
}

func TestAssignmentsAcceptanceTransactionFailureRollsBackDeliveryAndAssignmentsWithoutPendingAudit(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 16)
	b, _ := roomPeer(f, "alpha-two", 16)
	before := assignmentCounts(f)
	inboxExec(f, `CREATE TRIGGER reject_assignment BEFORE INSERT ON assignments WHEN EXISTS(SELECT 1 FROM assignments WHERE operator_message_id=NEW.operator_message_id) BEGIN SELECT RAISE(ABORT,'second assignment failure'); END`)
	request := mentionedRoomMessage("atomic-assignment", "alpha-one", "alpha-two")
	operatorEqual(t, "storage failure refused", roomOperator(f, request).text("type"), "error")
	for table, count := range before {
		operatorEqual(t, "failed transaction leaves "+table+" unchanged", f.count(table), count)
	}
	var pending int
	operatorNoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM operator_audit WHERE message_id='atomic-assignment' AND result='pending'`).Scan(&pending))
	operatorEqual(t, "failed transaction leaves no pending audit", pending, 0)
	inboxExec(f, `DROP TRIGGER reject_assignment`)
	roomRecipients(t, roomOperator(f, request), "alpha-one", "alpha-two")
	for _, peer := range []*sessionProbe{a, b} {
		operatorEqual(t, "first delivery belongs to successful transaction", peer.frame().text("message_id"), "atomic-assignment")
	}
	operatorEqual(t, "one message after successful retry", f.count("messages"), before["messages"]+1)
	operatorEqual(t, "two assignments after successful retry", f.count("assignments"), 2)
}

func TestAssignmentsAcceptanceFrozenRetryPrecedesConnectivityVersionAndMembership(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 16)
	b, _ := roomPeer(f, "alpha-two", 16)
	request := mentionedRoomMessage("frozen-assignment", "alpha-two", "alpha-one", "alpha-one")
	accepted := roomOperator(f, request)
	roomRecipients(t, accepted, "alpha-one", "alpha-two")
	a.frame()
	b.frame()
	before := claimCall(b, "claim_get", claimSpec{parent: "frozen-assignment"})
	initial := assignmentState(t, before, "alpha-one", "assigned")
	roomDisconnect(f, a, "alpha-one")
	roomChange(f, "remove", "alpha-one", "alpha")
	roomChange(f, "delete", "", "alpha")
	roomPeer(f, "alpha-one", 15)
	request["mentions"] = []string{"alpha-one", "alpha-two"}
	retry := roomOperator(f, request)
	roomRecipients(t, retry, "alpha-one", "alpha-two")
	operatorEqual(t, "retry is stored result", sessionDecode[bool](t, retry, "repeated"), true)
	operatorEqual(t, "retry sequence frozen", sessionDecode[uint64](t, retry, "seq"), sessionDecode[uint64](t, accepted, "seq"))
	after := claimCall(b, "claim_get", claimSpec{parent: "frozen-assignment"})
	operatorEqual(t, "retry does not reset assignment", assignmentState(t, after, "alpha-one", "assigned").Assigned, initial.Assigned)
	request["mentions"] = []string{"alpha-two"}
	operatorEqual(t, "different fingerprint conflicts before admission", roomOperator(f, request).text("code"), "duplicate_message")
	operatorEqual(t, "retry never creates another message", f.count("messages"), 1)
	operatorEqual(t, "retry never duplicates assignments", f.count("assignments"), 2)
}

func TestAssignmentsAcceptanceCapCountsUniqueTagsAndNeverNarrowsAudience(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	var ids []string
	var peers []*sessionProbe
	for i := 0; i < 17; i++ {
		id := fmt.Sprintf("worker-%02d", i)
		peer, _ := roomPeer(f, id, 16)
		ids, peers = append(ids, id), append(peers, peer)
	}
	request := roomMessage("sixteen-assigned", "*")
	request["type"], request["mentions"] = "operator_send", append(slices.Clone(ids[:16]), ids[0])
	roomRecipients(t, roomOperator(f, request), slices.Clone(ids)...)
	for i, peer := range peers {
		frame := peer.frame()
		_, assigned := frame["assignment"]
		operatorEqual(t, "assignment follows frozen canonical tags", assigned, i < 16)
	}
	operatorEqual(t, "sixteen canonical assignments", f.count("assignments"), 16)
	request["message_id"], request["mentions"] = "seventeen-refused", ids
	operatorEqual(t, "seventeen tags refused", roomOperator(f, request).text("type"), "error")
	operatorEqual(t, "refused cap creates no message", f.count("messages"), 1)
	operatorEqual(t, "refused cap creates no assignment", f.count("assignments"), 16)
}

func TestAssignmentsAcceptanceClientCannotForgeAssignmentMetadata(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a, _ := roomPeer(f, "alpha", 16)
	roomPeer(f, "beta", 16)
	for _, operator := range []bool{false, true} {
		request := roomMessage(fmt.Sprintf("forged-%t", operator), "*")
		request["assignment"] = map[string]string{"task": "reply", "state": "assigned"}
		var response sessionWire
		if operator {
			request["type"] = "operator_send"
			response = roomOperator(f, request)
		} else {
			response = roomSend(a, request)
		}
		operatorEqual(t, "forged assignment rejected", response.text("code"), "malformed_frame")
	}
	operatorEqual(t, "forgery creates no message", f.count("messages"), 0)
	operatorEqual(t, "forgery creates no assignment", f.count("assignments"), 0)
}

func TestAssignmentsAcceptanceGroupsAndLegacyMentionRowsRemainAttentionOnly(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 16)
	b, _ := roomPeer(f, "alpha-two", 15)
	group := roomMessage("legacy-attention", "")
	delete(group, "to")
	group["type"], group["group"], group["mentions"] = "operator_send", []string{"alpha-one", "alpha-two"}, []string{"alpha-two"}
	roomRecipients(t, roomOperator(f, group), "alpha-one", "alpha-two")
	for _, peer := range []*sessionProbe{a, b} {
		frame := peer.frame()
		requireMentions(t, frame, "alpha-two")
		if _, ok := frame["assignment"]; ok {
			t.Fatal("group attention became an assignment")
		}
	}
	operatorEqual(t, "group tags create no assignment", f.count("assignments"), 0)
	roomRecipients(t, roomOperator(f, mentionedRoomMessage("legacy-room-row", "alpha-one")), "alpha-one", "alpha-two")
	a.frame()
	b.frame()
	// Removing the new table's rows leaves the same persisted message shape
	// as a room mention created before assignments existed.
	inboxExec(f, `DELETE FROM assignments WHERE operator_message_id='legacy-room-row'`)
	roomDisconnect(f, a, "alpha-one")
	reconnected, _ := roomPeer(f, "alpha-one", 16)
	for range 2 {
		if _, ok := reconnected.frame()["assignment"]; ok {
			t.Fatal("legacy mention inferred an assignment without a record")
		}
	}
	operatorEqual(t, "legacy replay creates no assignment", f.count("assignments"), 0)
}

func TestAssignmentsAcceptanceSquareTagKeepsReadersAndAssignsOnlyTagged(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	assigned, _ := roomPeer(f, "alpha", 16)
	reader, _ := roomPeer(f, "beta", 15)
	request := roomMessage("assignment-square", "*")
	request["type"], request["mentions"] = "operator_send", []string{"alpha", "alpha"}
	roomRecipients(t, roomOperator(f, request), "alpha", "beta")
	tagged, untagged := assigned.frame(), reader.frame()
	for _, frame := range []sessionWire{tagged, untagged} {
		operatorEqual(t, "public destination retained", frame.text("to"), "*")
		operatorEqual(t, "local operator authority", frame.text("origin"), "operator")
		requireMentions(t, frame, "alpha")
	}
	var view struct{ Task, State string }
	operatorNoError(t, json.Unmarshal(tagged["assignment"], &view))
	operatorEqual(t, "canonical assigned task", view.Task, "reply")
	operatorEqual(t, "authoritative initial state", view.State, "assigned")
	if _, exists := untagged["assignment"]; exists {
		t.Fatal("unmentioned reader received an assignment")
	}
	operatorEqual(t, "assignment never inserts an ordinary claim", f.count("claims"), 0)
}
