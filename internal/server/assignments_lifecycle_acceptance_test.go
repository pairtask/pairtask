//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func assignmentFinished(record assignmentAcceptanceRecord) string {
	if record.Finished == nil {
		return ""
	}
	return *record.Finished
}

func TestAssignmentsLifecycleAcceptanceConcurrentAssigneesKeepIndependentCanonicalReply(t *testing.T) {
	f, a, b, _ := assignedQuestion(t, 16)
	peers := []*sessionProbe{a, b}
	for _, action := range []string{"claim_set", "claim_done"} {
		start, writes := make(chan struct{}), make(chan error, 2)
		for i, peer := range peers {
			go func(i int, peer *sessionProbe) {
				<-start
				if err := peer.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
					writes <- err
					return
				}
				request := map[string]any{"type": action, "request_id": fmt.Sprintf("%s-%d", action, i),
					"operator_message_id": "assigned-question", "task": "reply"}
				if action == "claim_set" {
					request["role"], request["topic"] = "owner", "Answer the operator"
				}
				writes <- json.NewEncoder(peer.Conn).Encode(request)
			}(i, peer)
		}
		close(start)
		for range peers {
			operatorNoError(t, <-writes)
		}
		for i, peer := range peers {
			response := peer.frame()
			operatorEqual(t, "independent reply action accepted", response.text("type"), "claim_result")
			operatorEqual(t, "correlation retained", response.text("request_id"), fmt.Sprintf("%s-%d", action, i))
		}
	}
	final := claimCall(a, "claim_get", claimSpec{parent: "assigned-question"})
	assignmentState(t, final, "alpha", "done")
	assignmentState(t, final, "beta", "done")
	operatorEqual(t, "canonical task has no competing ordinary claim", f.count("claims"), 0)
	operatorEqual(t, "one assignment per logical agent", f.count("assignments"), 2)
}

func assignedQuestion(t *testing.T, readerVersion int) (*operatorFixture, *sessionProbe, *sessionProbe, *sessionProbe) {
	t.Helper()
	f := newOperatorFixture(t, os.Getuid(), "")
	a, _ := roomPeer(f, "alpha", 16)
	b, _ := roomPeer(f, "beta", 16)
	c, _ := roomPeer(f, "reader", readerVersion)
	request := roomMessage("assigned-question", "*")
	request["type"], request["mentions"] = "operator_send", []string{"beta", "alpha"}
	roomRecipients(t, roomOperator(f, request), "alpha", "beta", "reader")
	for _, peer := range []*sessionProbe{a, b, c} {
		peer.frame()
	}
	return f, a, b, c
}

func TestAssignmentsLifecycleAcceptanceEveryOwnTransitionAndTerminalState(t *testing.T) {
	for _, initial := range []string{"assigned", "released", "done"} {
		for _, action := range []string{"claim_set", "claim_release", "claim_done"} {
			t.Run(initial+"/"+action, func(t *testing.T) {
				f, a, b, _ := assignedQuestion(t, 16)
				spec := claimSpec{parent: "assigned-question", task: "reply", role: "owner"}
				if initial != "assigned" {
					first := "claim_release"
					if initial == "done" {
						first = "claim_done"
					}
					operatorEqual(t, "initial transition accepted", claimCall(a, first, spec).text("type"), "claim_result")
				}
				before := assignmentState(t, claimCall(a, "claim_get", spec), "alpha", initial)
				response := claimCall(a, action, spec)
				allowed := initial == "assigned" || initial == "released" && action == "claim_release" || initial == "done" && action == "claim_done"
				wantState := initial
				if allowed {
					operatorEqual(t, "own transition accepted", response.text("type"), "claim_result")
					if action == "claim_release" {
						wantState = "released"
					} else if action == "claim_done" {
						wantState = "done"
					}
				} else {
					operatorEqual(t, "terminal transition refused", response.text("code"), "claim_conflict")
				}
				after := assignmentState(t, response, "alpha", wantState)
				operatorEqual(t, "assignment age never resets", after.Assigned, before.Assigned)
				if initial == wantState {
					operatorEqual(t, "idempotent or refused finish time unchanged", assignmentFinished(after), assignmentFinished(before))
				}
				operatorEqual(t, "terminal states carry finish time", after.Finished != nil, wantState != "assigned")
				assignmentState(t, claimCall(b, "claim_get", spec), "beta", "assigned")
				operatorEqual(t, "assignment actions never create claim rows", f.count("claims"), 0)
			})
		}
	}
}

func TestAssignmentsLifecycleAcceptanceOnlyOwnerReplyInterceptedAndRolesCannotBeForged(t *testing.T) {
	f, a, b, reader := assignedQuestion(t, 16)
	response := claimCall(reader, "claim_set", claimSpec{parent: "assigned-question", task: "reply", role: "owner"})
	operatorEqual(t, "unassigned owner refused", response.text("code"), "claim_conflict")
	assignmentState(t, response, "alpha", "assigned")
	assignmentState(t, response, "beta", "assigned")
	for _, kind := range []string{"claim_set", "claim_release", "claim_done"} {
		operatorEqual(t, "client cannot invent assignee role", claimCall(a, kind, claimSpec{"assigned-question", "reply", "assignee"}).text("code"), "invalid_claim")
	}
	operatorEqual(t, "reply reviewer unaffected", claimCall(reader, "claim_set", claimSpec{"assigned-question", "reply", "reviewer"}).text("type"), "claim_result")
	operatorEqual(t, "other owner task unaffected", claimCall(a, "claim_set", claimSpec{"assigned-question", "implementation", "owner"}).text("type"), "claim_result")
	operatorEqual(t, "only ordinary roles create claim rows", f.count("claims"), 2)
	for _, peer := range []*sessionProbe{a, b} {
		operatorEqual(t, "each assignee may decline", claimCall(peer, "claim_release", claimSpec{"assigned-question", "reply", "owner"}).text("type"), "claim_result")
	}
	response = claimCall(reader, "claim_set", claimSpec{"assigned-question", "reply", "owner"})
	operatorEqual(t, "all released does not open ordinary claim", response.text("code"), "claim_conflict")
	assignmentState(t, response, "alpha", "released")
	assignmentState(t, response, "beta", "released")
	operatorEqual(t, "declines cannot create an ordinary owner", f.count("claims"), 2)
}

func TestAssignmentsLifecycleAcceptanceReconnectCarriesCurrentLogicalAssignment(t *testing.T) {
	for _, state := range []string{"assigned", "released", "done"} {
		t.Run(state, func(t *testing.T) {
			f, a, _, _ := assignedQuestion(t, 16)
			if state != "assigned" {
				action := "claim_release"
				if state == "done" {
					action = "claim_done"
				}
				claimCall(a, action, claimSpec{"assigned-question", "reply", "owner"})
			}
			before := assignmentState(t, claimCall(a, "claim_get", claimSpec{parent: "assigned-question"}), "alpha", state)
			roomDisconnect(f, a, "alpha")
			reconnected, _ := roomPeer(f, "alpha", 16)
			replay := reconnected.frame()
			operatorEqual(t, "durable replay", sessionDecode[bool](t, replay, "replayed"), true)
			view := sessionDecode[map[string]string](t, replay, "assignment")
			operatorEqual(t, "current state on replay", view["state"], state)
			operatorEqual(t, "canonical task on replay", view["task"], "reply")
			after := assignmentState(t, claimCall(reconnected, "claim_get", claimSpec{parent: "assigned-question"}), "alpha", state)
			operatorEqual(t, "logical assignment retains timestamp", after.Assigned, before.Assigned)
			operatorEqual(t, "reconnect never adds assignments", f.count("assignments"), 2)
		})
	}
}

func TestAssignmentsLifecycleAcceptanceOldReaderGetsUnchangedClaimPage(t *testing.T) {
	_, _, _, reader := assignedQuestion(t, 15)
	result := claimCall(reader, "claim_get", claimSpec{parent: "assigned-question"})
	operatorEqual(t, "old read still supported", result.text("type"), "claim_result")
	for _, key := range []string{"assignments", "assignments_total", "assignments_truncated"} {
		if _, exists := result[key]; exists {
			t.Fatalf("v15 caller received v16 field %s: %s", key, sessionJSON(result))
		}
	}
	operatorEqual(t, "assignment count never folds into claims", sessionDecode[int](t, result, "claims_total"), 0)
}

func TestAssignmentsLifecycleAcceptanceEveryReaderMustReplyInParentAudience(t *testing.T) {
	for _, room := range []string{"", "alpha"} {
		t.Run("parent-room="+room, func(t *testing.T) {
			f := roomFixture(t)
			a, _ := roomPeer(f, "alpha-one", 16)
			reader, _ := roomPeer(f, "alpha-two", 15)
			request := roomMessage("audience-parent", "*")
			request["type"], request["mentions"] = "operator_send", []string{"alpha-one"}
			if room != "" {
				request["room"] = room
			}
			roomRecipients(t, roomOperator(f, request), "alpha-one", "alpha-two")
			a.frame()
			reader.frame()
			for i, sender := range []*sessionProbe{a, reader} {
				group := roomMessage(fmt.Sprintf("unsupported-group-%d", i), "*")
				group["in_reply_to"], group["group"] = "audience-parent", []string{"alpha-one", "alpha-two"}
				operatorEqual(t, "explicit group remains operator-only", roomSend(sender, group).text("code"), "malformed_frame")
				delete(group, "to")
				group["message_id"] = fmt.Sprintf("missing-group-destination-%d", i)
				operatorEqual(t, "group-only retains missing destination validation", roomSend(sender, group).text("code"), "missing_recipient")
				for j, change := range []func(map[string]any){
					func(m map[string]any) { m["to"] = "operator" },
					func(m map[string]any) { m["to"] = "alpha-one" },
					func(m map[string]any) { m["room"] = "beta" },
				} {
					message := roomMessage(fmt.Sprintf("wrong-%d-%d", i, j), "*")
					message["in_reply_to"] = "audience-parent"
					change(message)
					operatorEqual(t, "explicit audience mismatch rejected", roomSend(sender, message).text("code"), "invalid_reply_audience")
				}
				message := roomMessage(fmt.Sprintf("public-answer-%d", i), "*")
				message["in_reply_to"] = "audience-parent"
				other, otherID := reader, "alpha-two"
				if i == 1 {
					other, otherID = a, "alpha-one"
				}
				roomRecipients(t, roomSend(sender, message), otherID, "operator")
				for _, recipient := range []*sessionProbe{other} {
					frame := recipient.frame()
					operatorEqual(t, "public answer inherits parent room", frame.text("room"), room)
					operatorEqual(t, "agent answer has no operator authority", frame.text("origin"), "agent")
					if _, exists := frame["assignment"]; exists {
						t.Fatal("agent reply carried operator assignment metadata")
					}
				}
			}
			operatorEqual(t, "only parent and two valid replies stored", f.count("messages"), 3)
			operatorEqual(t, "unassigned readers need no claims", f.count("claims"), 0)
			operatorEqual(t, "refused groups never change assignments", f.count("assignments"), 1)
		})
	}
}
