//go:build linux

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func assignmentStatus(t *testing.T, f *operatorFixture) sessionWire {
	t.Helper()
	p := &sessionProbe{operatorPeer: operatorDial(t, f.agent)}
	defer p.Close()
	p.send(map[string]any{"type": "status"})
	response := p.frame()
	operatorEqual(t, "read-only status", response.text("type"), "status")
	return sessionDecode[sessionWire](t, response, "status")
}

func assignmentParentPage(t *testing.T, surface sessionWire, parent string) sessionWire {
	t.Helper()
	for _, page := range sessionDecode[[]sessionWire](t, surface, "claims") {
		if page.text("operator_message_id") == parent {
			return page
		}
	}
	t.Fatalf("surface lost assignment-only parent %s: %s", parent, sessionJSON(surface))
	return nil
}

func TestAssignmentsSurfacesAcceptanceHistoryStatusSnapshotAndChangeEvents(t *testing.T) {
	f, a, _, _ := assignedQuestion(t, 16)
	observer := sessionOpen(f)
	observer.snapshot() // event delivery begins after the initial snapshot
	beforeMessages, beforeAgents := f.count("messages"), f.count("agents")
	operatorEqual(t, "release accepted before event", claimCall(a, "claim_release", claimSpec{"assigned-question", "reply", "owner"}).text("type"), "claim_result")
	event := observer.frame()
	payload := sessionDecode[sessionWire](t, event, "event")
	operatorEqual(t, "assignment change event", payload.text("kind"), "claims_changed")
	operatorEqual(t, "event names parent", payload.text("operator_message_id"), "assigned-question")
	snapshot, _ := observer.request("operator_snapshot", nil)
	view := sessionDecode[sessionWire](t, snapshot, "snapshot")
	for _, surface := range []sessionWire{view, assignmentStatus(t, f)} {
		page := assignmentParentPage(t, surface, "assigned-question")
		operatorEqual(t, "one parent counted once", sessionDecode[int](t, surface, "claims_total"), 1)
		operatorEqual(t, "ordinary claim total unchanged", sessionDecode[int](t, page, "claims_total"), 0)
		operatorEqual(t, "separate assignment total", sessionDecode[int](t, page, "assignments_total"), 2)
		assignmentState(t, page, "alpha", "released")
		assignmentState(t, page, "beta", "assigned")
	}
	history := historyRead(f, 0, 0, 16)
	operatorEqual(t, "only actual parent in history", len(history.Entries), 1)
	parent := history.Entries[0].Message
	operatorEqual(t, "history retains public parent", parent.To, "*")
	if len(parent.Mentions) != 2 || parent.Mentions[0] != "alpha" || parent.Mentions[1] != "beta" {
		t.Fatalf("history lost canonical mentions: %+v", parent)
	}
	encoded, err := json.Marshal(parent)
	operatorNoError(t, err)
	var fields map[string]any
	operatorNoError(t, json.Unmarshal(encoded, &fields))
	if _, exists := fields["assignment"]; exists {
		t.Fatal("operator history invented a recipient-specific assignment")
	}
	operatorEqual(t, "surface reads create no messages", f.count("messages"), beforeMessages)
	operatorEqual(t, "surface reads register no agents", f.count("agents"), beforeAgents)
}

func TestAssignmentsSurfacesAcceptanceCombinedSnapshotBoundsCountParentsOnce(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 33; i++ {
		_, err := f.db.Exec(`INSERT INTO assignments(operator_message_id,agent_id,task,state,assigned_at) VALUES(?,?,?,?,?)`,
			fmt.Sprintf("parent-%02d", i), "alpha", "reply", "assigned", base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339))
		operatorNoError(t, err)
	}
	_, err := f.db.Exec(`INSERT INTO claims(operator_message_id,task,role,agent_id,topic,state,claimed_at) VALUES(?,?,?,?,?,?,?)`,
		"parent-32", "review", "reviewer", "reader", "Check reply", "active", base.Add(32*time.Minute).Format(time.RFC3339))
	operatorNoError(t, err)
	for i := 1; i < 20; i++ {
		_, err := f.db.Exec(`INSERT INTO assignments(operator_message_id,agent_id,task,state,assigned_at) VALUES(?,?,?,?,?)`,
			"parent-32", fmt.Sprintf("worker-%02d", i), "reply", "assigned", base.Add(32*time.Minute).Format(time.RFC3339))
		operatorNoError(t, err)
	}
	observer := sessionOpen(f)
	response, _ := observer.request("operator_snapshot", nil)
	for _, surface := range []sessionWire{sessionDecode[sessionWire](t, response, "snapshot"), assignmentStatus(t, f)} {
		operatorEqual(t, "union of claimed and assigned parents", sessionDecode[int](t, surface, "claims_total"), 33)
		operatorEqual(t, "global truncation explicit", sessionDecode[bool](t, surface, "claims_truncated"), true)
		pages := sessionDecode[[]sessionWire](t, surface, "claims")
		operatorEqual(t, "at most32 unique parents", len(pages), 32)
		operatorEqual(t, "newest assignment parent first", pages[0].text("operator_message_id"), "parent-32")
		seen := map[string]bool{}
		for _, page := range pages {
			id := page.text("operator_message_id")
			if seen[id] {
				t.Fatalf("parent counted twice: %s", id)
			}
			seen[id] = true
		}
		newest := assignmentParentPage(t, surface, "parent-32")
		operatorEqual(t, "claims count remains separate", sessionDecode[int](t, newest, "claims_total"), 1)
		operatorEqual(t, "assignment count retained", sessionDecode[int](t, newest, "assignments_total"), 20)
		operatorEqual(t, "assignment truncation explicit", sessionDecode[bool](t, newest, "assignments_truncated"), true)
		operatorEqual(t, "assignment per-parent bound", len(assignmentPage(t, newest)), 16)
	}
}
