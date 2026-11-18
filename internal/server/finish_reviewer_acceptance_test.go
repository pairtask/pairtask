//go:build linux

package server

import (
	"os"
	"testing"
)

func reviewerClaimState(t *testing.T, response sessionWire, role, agent, want string) {
	t.Helper()
	for _, record := range claimRecords(t, response) {
		if record.Role == role && record.Agent == agent {
			operatorEqual(t, role+" state", record.State, want)
			return
		}
	}
	t.Fatalf("missing %s claim held by %s", role, agent)
}

func TestFinishReviewerAcceptanceCompletionIsRoleScopedAndTerminal(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	owner, reviewer, outsider := claimPeer(f, "owner"), claimPeer(f, "reviewer"), claimPeer(f, "outsider")
	claimParent(t, f, "review-work", owner, reviewer, outsider)
	own := claimSpec{parent: "review-work", task: "implementation", role: "owner"}
	rev := claimSpec{parent: "review-work", task: "implementation", role: "reviewer"}
	operatorEqual(t, "owner acquired", claimCall(owner, "claim_set", own).text("type"), "claim_result")
	operatorEqual(t, "reviewer acquired", claimCall(reviewer, "claim_set", rev).text("type"), "claim_result")
	operatorEqual(t, "outsider cannot finish review", claimCall(outsider, "claim_done", rev).text("code"), "invalid_claim")
	done := claimCall(reviewer, "claim_done", rev)
	operatorEqual(t, "reviewer finishes own review", done.text("type"), "claim_result")
	reviewerClaimState(t, done, "reviewer", "reviewer", "done")
	reviewerClaimState(t, done, "owner", "owner", "active")
	operatorEqual(t, "finished review cannot be reclaimed", claimCall(outsider, "claim_set", rev).text("code"), "claim_conflict")
	operatorEqual(t, "finished review cannot be released", claimCall(reviewer, "claim_release", rev).text("code"), "invalid_claim")
	own.role = ""
	ownerDone := claimCall(owner, "claim_done", own)
	operatorEqual(t, "omitted role still finishes owner", ownerDone.text("type"), "claim_result")
	reviewerClaimState(t, ownerDone, "owner", "owner", "done")
	reviewerClaimState(t, claimCall(outsider, "claim_get", rev), "reviewer", "reviewer", "done")
}

func TestFinishReviewerAcceptanceExplicitReviewerCannotFinishOwnOwnerClaim(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := claimPeer(f, "worker")
	claimParent(t, f, "wrong-role", a)
	spec := claimSpec{parent: "wrong-role", task: "reply", role: "owner"}
	operatorEqual(t, "owner acquired", claimCall(a, "claim_set", spec).text("type"), "claim_result")
	spec.role = "reviewer"
	operatorEqual(t, "explicit reviewer never aliases owner", claimCall(a, "claim_done", spec).text("code"), "invalid_claim")
	reviewerClaimState(t, claimCall(a, "claim_get", spec), "owner", "worker", "active")
}

func TestFinishReviewerAcceptanceReplyReviewDoesNotFinishAssignments(t *testing.T) {
	f, a, b, reviewer := assignedQuestion(t, 16)
	rev := claimSpec{parent: "assigned-question", task: "reply", role: "reviewer"}
	operatorEqual(t, "review acquired alongside assignments", claimCall(reviewer, "claim_set", rev).text("type"), "claim_result")
	done := claimCall(reviewer, "claim_done", rev)
	operatorEqual(t, "reply reviewer finishes", done.text("type"), "claim_result")
	reviewerClaimState(t, done, "reviewer", "reader", "done")
	assignmentState(t, done, "alpha", "assigned")
	assignmentState(t, done, "beta", "assigned")
	operatorEqual(t, "only review uses ordinary claim row", f.count("claims"), 1)
	own := claimSpec{parent: "assigned-question", task: "reply"}
	operatorEqual(t, "default owner assignment still finishes", claimCall(a, "claim_done", own).text("type"), "claim_result")
	final := claimCall(b, "claim_get", own)
	assignmentState(t, final, "alpha", "done")
	assignmentState(t, final, "beta", "assigned")
	reviewerClaimState(t, final, "reviewer", "reader", "done")
}
