//go:build linux

package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

type acceptanceReviewAdvisory struct {
	Code  string     `json:"code"`
	At    *time.Time `json:"at"`
	AskID string     `json:"ask_id"`
}

func advisoriesQueue(t *testing.T, f *reviewSignalFixture, id, reviewer string) []acceptanceReviewAdvisory {
	t.Helper()
	ask := protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "304"}, Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}
	r, _ := f.owner.request(protocol.TypeOperatorContact, map[string]any{
		"contact_op": protocol.ContactOpReviewAsk, "contact_key": f.key,
		"contact_to": []string{reviewer}, "message_id": id, "review_ask": ask,
	})
	operatorEqual(t, "advisory never blocks valid queue", r.text("type"), protocol.TypeOperatorContactResult)
	kept, found, err := f.st.PeerMessages().AskOutFor(context.Background(), f.key, id)
	operatorNoError(t, err)
	operatorEqual(t, "request actually queued", found, true)
	operatorEqual(t, "request targets original reviewer", kept.Reviewer, reviewer)
	operatorEqual(t, "advisory never rewrites explicit head", kept.Ask.Head, ask.Head)
	var notes []acceptanceReviewAdvisory
	if raw := r["review_advisories"]; len(raw) > 0 {
		operatorNoError(t, json.Unmarshal(raw, &notes))
	}
	return notes
}

func advisoriesWant(t *testing.T, notes []acceptanceReviewAdvisory, code, askID string, at time.Time) {
	t.Helper()
	operatorEqual(t, "one scoped advisory", len(notes), 1)
	operatorEqual(t, "advisory code", notes[0].Code, code)
	operatorEqual(t, "advisory identifies prior request only when relevant", notes[0].AskID, askID)
	if notes[0].At == nil {
		t.Fatal("advisory lacks evidence timestamp")
	}
	operatorEqual(t, "advisory uses actual evidence timestamp", notes[0].At.Equal(at), true)
}

func TestReviewAdvisoriesServerAcceptanceDirectoryHintRequiresKnownObservation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		known  bool
		agents []string
		warn   bool
	}{{"never-received", false, nil, false}, {"listed", true, []string{"reviewer"}, false}, {"known-empty", true, []string{}, true}, {"other-agent", true, []string{"other"}, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReviewSignalFixture(t)
			at := time.Now().UTC().Add(-time.Minute)
			if tc.known {
				operatorNoError(t, f.st.PeerMessages().SetPeerDirectory(context.Background(), f.key, tc.agents, at))
			}
			notes := advisoriesQueue(t, f, "directory-hint", "reviewer")
			if tc.warn {
				advisoriesWant(t, notes, "not_in_directory", "", at)
			} else {
				operatorEqual(t, "no unsupported directory claim", len(notes), 0)
			}
		})
	}
}

func TestReviewAdvisoriesServerAcceptanceLocalQueueDoesNotClearRemoteRefusal(t *testing.T) {
	f := newReviewSignalFixture(t)
	operatorEqual(t, "no refusal initially", len(advisoriesQueue(t, f, "first", "reviewer")), 0)
	err := f.st.PeerMessages().Refused(context.Background(), f.key, messaging.Refused{ID: "first", Refusal: messaging.Refuse(messaging.ReasonNotShared)})
	operatorNoError(t, err)
	refused, found, err := f.st.PeerMessages().AskOutFor(context.Background(), f.key, "first")
	operatorNoError(t, err)
	operatorEqual(t, "refused request retained", found, true)
	operatorEqual(t, "real refusal applied", refused.State, "refused")
	at := refused.UpdatedAt
	advisoriesWant(t, advisoriesQueue(t, f, "second", "reviewer"), "refused_not_shared", "first", at)
	advisoriesWant(t, advisoriesQueue(t, f, "third", "reviewer"), "refused_not_shared", "first", at)
	operatorEqual(t, "refusal belongs only to its reviewer", len(advisoriesQueue(t, f, "other-scope", "other")), 0)
	id := messageIdentity(t)
	messageConfirm(f.redeemOperatorFixture, id, 2)
	other := *f
	other.key = id.Fingerprint()
	operatorEqual(t, "refusal belongs only to its contact", len(advisoriesQueue(t, &other, "other-contact", "reviewer")), 0)
	invalid := messaging.ReviewOutcome{AskID: "second", Reviewer: "reviewer", Seq: 1, State: "accepted"} // missing required attempt
	a, err := f.st.PeerMessages().AcceptOutcome(context.Background(), f.key, invalid, at.Add(time.Second))
	operatorNoError(t, err)
	operatorEqual(t, "invalid remote progress rejected", a.Stored, false)
	advisoriesWant(t, advisoriesQueue(t, f, "after-invalid", "reviewer"), "refused_not_shared", "first", at)
	progress := messaging.ReviewOutcome{AskID: "second", Reviewer: "reviewer", Seq: 1, State: "requested"}
	a, err = f.st.PeerMessages().AcceptOutcome(context.Background(), f.key, progress, at.Add(2*time.Second))
	operatorNoError(t, err)
	operatorEqual(t, "new remote progress applied", a.Stored, true)
	operatorEqual(t, "new remote progress clears refusal", len(advisoriesQueue(t, f, "after-progress", "reviewer")), 0)
}

func TestReviewAdvisoriesServerAcceptanceReplayDoesNotReadLaterEvidence(t *testing.T) {
	f := newReviewSignalFixture(t)
	operatorEqual(t, "first queue has no known evidence", len(advisoriesQueue(t, f, "stable", "reviewer")), 0)
	at := time.Now().UTC()
	operatorNoError(t, f.st.PeerMessages().SetPeerDirectory(context.Background(), f.key, []string{}, at))
	operatorEqual(t, "replay does not reevaluate later evidence", len(advisoriesQueue(t, f, "stable", "reviewer")), 0)
	advisoriesWant(t, advisoriesQueue(t, f, "fresh", "reviewer"), "not_in_directory", "", at)
}

func TestReviewAdvisoriesServerAcceptanceAgentAcceptedStageCarriesQueueAdvice(t *testing.T) {
	f := newReviewSignalFixture(t)
	ctx := context.Background()
	at := time.Now().UTC()
	_, err := f.st.PeerMessages().Share(ctx, f.key, []string{"sender"}, at)
	operatorNoError(t, err)
	operatorNoError(t, f.st.PeerMessages().SetPeerDirectory(ctx, f.key, []string{}, at))
	agent := f.peer("sender", protocol.Version)
	ask := &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "304"}, Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}
	agent.write(protocol.Message{Type: protocol.TypeMsg, MessageID: "agent-advice", Workspace: f.key, To: "reviewer", ReviewAsk: ask, Durable: true, AckRequested: true})
	response := agent.read()
	operatorEqual(t, "valid agent request accepted despite advice", response.Stage, protocol.StageAccepted)
	data, err := json.Marshal(response)
	operatorNoError(t, err)
	var decoded struct {
		Notes []acceptanceReviewAdvisory `json:"review_advisories"`
	}
	operatorNoError(t, json.Unmarshal(data, &decoded))
	advisoriesWant(t, decoded.Notes, "not_in_directory", "", at)
	kept, found, err := f.st.PeerMessages().AskOutFor(ctx, f.key, "agent-advice")
	operatorNoError(t, err)
	operatorEqual(t, "accepted stage accompanies real stored request", found, true)
	operatorEqual(t, "request attribution stays with sender", kept.RequesterAgent, "sender")
	operatorEqual(t, "advice never rewrites agent pins", kept.Ask.Head, ask.Head)
}
