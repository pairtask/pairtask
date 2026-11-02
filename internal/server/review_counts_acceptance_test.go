//go:build linux

package server

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func reviewCountsSnapshot(t *testing.T, owner *sessionProbe) map[string]json.RawMessage {
	t.Helper()
	response, _ := owner.request("operator_snapshot", nil)
	operatorEqual(t, "snapshot result", response.text("type"), "operator_snapshot")
	snapshot := sessionDecode[map[string]json.RawMessage](t, response, "snapshot")
	raw, present := snapshot["review_counts"]
	if !present || string(raw) == "null" {
		t.Fatal("capable broker snapshot omits authoritative review_counts")
	}
	var counts map[string]json.RawMessage
	operatorNoError(t, json.Unmarshal(raw, &counts))
	return counts
}

type reviewSignalFixture struct {
	*redeemOperatorFixture
	owner *sessionProbe
	key   string
}

func newReviewSignalFixture(t *testing.T) *reviewSignalFixture {
	t.Helper()
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	_, err := f.st.PeerMessages().SetReviewGrant(context.Background(), id.Fingerprint(), store.ReviewGrantChange{Agents: []string{"reviewer"}, Mode: store.ReviewAsk, Now: time.Now()})
	operatorNoError(t, err)
	owner := sessionOpen(f.operatorFixture)
	owner.snapshot()
	return &reviewSignalFixture{f, owner, id.Fingerprint()}
}

func (f *reviewSignalFixture) receive(m messaging.Message) {
	f.t.Helper()
	a, err := f.st.PeerMessages().Accept(context.Background(), f.key, m, m.CreatedAt)
	operatorNoError(f.t, err)
	operatorEqual(f.t, "incoming review staged", a.Stored, true)
}

func reviewSignalAsk(id string, n uint64, at time.Time) messaging.Message {
	return messaging.Message{ID: id, N: n, CreatedAt: at, Text: "review pending", From: "requester", To: []string{"reviewer"}, Review: &messaging.ReviewAsk{Forge: "github", Repository: "pairtask/pairtask", Change: "286", Base: "base", Head: "head"}}
}

func (f *reviewSignalFixture) field(name string) string {
	f.t.Helper()
	return string(reviewCountsSnapshot(f.t, f.owner)[name])
}

func (f *reviewSignalFixture) decline(id string) {
	f.t.Helper()
	_, err := f.st.DecideRemoteReview(context.Background(), store.RemoteDecision{Contact: f.key, RemoteID: id, Approve: false, Now: time.Now()})
	operatorNoError(f.t, err)
}

func TestReviewCountsAcceptanceAwaitingIdentityIgnoresTimestampOrdering(t *testing.T) {
	for _, delta := range []time.Duration{0, -time.Hour} {
		t.Run(delta.String(), func(t *testing.T) {
			f := newReviewSignalFixture(t)
			at := time.Now()
			f.receive(reviewSignalAsk("first", 1, at))
			first := f.field("awaiting_token")
			operatorEqual(t, "first pending count", f.field("awaiting_you"), "1")
			operatorEqual(t, "nonempty set has identity", first != `""`, true)
			operatorEqual(t, "identity stable on reread", f.field("awaiting_token"), first)
			f.decline("first")
			f.receive(reviewSignalAsk("replacement", 2, at.Add(delta)))
			operatorEqual(t, "same pending count", f.field("awaiting_you"), "1")
			operatorEqual(t, "different set changes identity despite equal or backward clock", f.field("awaiting_token") != first, true)
			f.decline("replacement")
			operatorEqual(t, "empty set clears count", f.field("awaiting_you"), "0")
			operatorEqual(t, "empty set clears identity", f.field("awaiting_token"), `""`)
		})
	}
}

func TestReviewCountsAcceptancePendingCountsIgnoreDisplayedAndAcknowledgedRows(t *testing.T) {
	f := newReviewSignalFixture(t)
	f.receive(reviewSignalAsk("pending", 1, time.Now()))
	before := f.field("awaiting_token")
	_, err := f.st.PeerMessages().MarkRead(context.Background(), f.key, []string{"pending"}, time.Now())
	operatorNoError(t, err)
	_, _, err = f.st.PeerMessages().Ack(context.Background(), f.key, store.AckSelector{IDs: []string{"pending"}}, time.Now())
	operatorNoError(t, err)
	operatorEqual(t, "display/ACK not review decision", f.field("awaiting_you"), "1")
	operatorEqual(t, "display/ACK not new set", f.field("awaiting_token"), before)
}

func TestReviewCountsAcceptanceRunningIncludesUnacceptedAndPinned(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := reviewTestPeer(f, "alpha")
	reviewTestView(t, reviewTestRequest(f, "requested", reviewTestSpec()), "operator_review_result")
	reviewTestView(t, reviewTestRequest(f, "ready", reviewTestSpec()), "operator_review_result")
	reviewTestView(t, reviewTestCall(p, "review_prepare", "ready", map[string]any{"revisions": reviewTestRevisions()}), "review_result")
	reviewTestStart(f, p, "running")
	owner := sessionOpen(f)
	operatorEqual(t, "requested ready and in_review counted", string(reviewCountsSnapshot(t, owner)["running"]), "3")
	reviewTestView(t, reviewTestOperator(f, map[string]any{"review_op": "cancel", "review_id": "requested"}), "operator_review_result")
	operatorEqual(t, "terminal no longer running", string(reviewCountsSnapshot(t, owner)["running"]), "2")
}

func TestReviewCountsAcceptanceAskedOpenIncludesUnloadedRequestsAndExcludesSettled(t *testing.T) {
	f := newReviewSignalFixture(t)
	m := reviewSignalAsk("outbound", 1, time.Now())
	_, err := f.st.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: m.ID, Text: m.Text, To: m.To, Review: m.Review, Now: m.CreatedAt})
	operatorNoError(t, err)
	operatorEqual(t, "outbound ask counted before any page loaded", f.field("asked_open"), "1")
	a, err := f.st.PeerMessages().AcceptOutcome(context.Background(), f.key, messaging.ReviewOutcome{AskID: m.ID, Reviewer: "reviewer", Seq: 1, State: "declined", Reason: "unavailable"}, time.Now())
	operatorNoError(t, err)
	operatorEqual(t, "terminal outcome saved", a.Stored, true)
	operatorEqual(t, "settled request no longer open", f.field("asked_open"), "0")
	operatorEqual(t, "incoming and outgoing concepts remain distinct", f.field("awaiting_you"), "0")
}

func TestReviewCountsAcceptanceRefreshMatchesSnapshotWithoutMutation(t *testing.T) {
	f := newReviewSignalFixture(t)
	f.receive(reviewSignalAsk("pending", 1, time.Now()))
	before := reviewCountsSnapshot(t, f.owner)
	response := reviewPendingPage(f, map[string]any{"review_op": "counts"})
	operatorEqual(t, "counts read succeeds", response.text("type"), "operator_review_result")
	operatorEqual(t, "result echoes operation", response.text("review_op"), "counts")
	after := sessionDecode[map[string]json.RawMessage](t, response, "review_counts")
	for _, field := range []string{"awaiting_you", "awaiting_token", "running", "asked_open"} {
		operatorEqual(t, "refresh matches baseline "+field, string(after[field]), string(before[field]))
	}
	operatorEqual(t, "counts read never dispatches", f.count("messages"), 0)
	operatorEqual(t, "counts read never creates reviews", f.count("reviews"), 0)
}

func TestReviewCountsAcceptanceRefreshRejectsUnrelatedFields(t *testing.T) {
	for _, extra := range []map[string]any{{"review_before": "cursor"}, {"review_id": "other"}, {"review_reason": "approve"}, {"contact_key": "friendly"}} {
		t.Run(sessionJSON(extra), func(t *testing.T) {
			f := newReviewSignalFixture(t)
			extra["review_op"] = "counts"
			response := reviewPendingPage(f, extra)
			operatorEqual(t, "unrelated field refused", response.text("code"), "malformed_frame")
			operatorEqual(t, "bad count read does not create review", f.count("reviews"), 0)
		})
	}
}

func TestReviewCountsAcceptanceEmptySnapshotKeepsAllZeroFields(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	owner := sessionOpen(f)
	counts := reviewCountsSnapshot(t, owner)
	for _, field := range []string{"awaiting_you", "running", "asked_open"} {
		raw, present := counts[field]
		operatorEqual(t, "zero count remains present: "+field, present, true)
		operatorEqual(t, "zero count encoded honestly: "+field, string(raw), "0")
	}
	operatorEqual(t, "empty identity token remains present", string(counts["awaiting_token"]), `""`)
}
