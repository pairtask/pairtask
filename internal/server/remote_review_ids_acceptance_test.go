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

func TestRemoteReviewIDsServerAcceptanceNoticeNamesBothIDsWithoutChangingRoles(t *testing.T) {
	f := newNoticeGuidanceServer(t)
	var body struct {
		Text string `json:"text"`
	}
	operatorNoError(t, json.Unmarshal(f.notice.Payload, &body))
	for _, part := range []string{"(their ask ID guidance-ask)", "review_id " + f.notice.ReviewID, "message_id " + f.notice.MessageID} {
		operatorEqual(t, "notice includes distinct ID role "+part, strings.Contains(body.Text, part), true)
	}
	operatorEqual(t, "notice identity separate from review", f.notice.MessageID, "review:"+f.notice.ReviewID)
	rec, found, err := f.st.GetReview(context.Background(), f.notice.ReviewID)
	operatorNoError(t, err)
	operatorEqual(t, "local review exists", found, true)
	operatorEqual(t, "original requester ID retained", rec.View.RemoteRequest, "guidance-ask")
}

func reportedReviewID(t *testing.T, f *outcomeNoticeServer, id string) protocol.Message {
	t.Helper()
	a, err := f.st.PeerMessages().AcceptOutcome(context.Background(), f.key, messaging.ReviewOutcome{AskID: "ask", Reviewer: "target", Seq: 2, Attempt: 1, State: "accepted", ReviewID: id}, time.Now())
	operatorNoError(t, err)
	operatorEqual(t, "identified outcome accepted", a.Stored, true)
	f.rescan("import-identified-outcome")
	notice := f.agent.read()
	if notice.ReviewOutcome == nil {
		t.Fatal("identified outcome notice missing metadata")
	}
	return notice
}

func TestRemoteReviewIDsServerAcceptanceRequesterViewsExposeLearnedForeignID(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	notice := reportedReviewID(t, f, "reviewer-local-id")
	operatorEqual(t, "notice carries remote review ID", notice.ReviewOutcome.ReviewID, "reviewer-local-id")
	operatorEqual(t, "notice still names requester ask", notice.ReviewOutcome.AskID, "ask")
	f.agent.write(protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "asked"})
	response := f.agent.read()
	operatorEqual(t, "requester asked view", response.Type, protocol.TypeReviewsAskedResult)
	operatorEqual(t, "single original ask", len(response.ReviewsAsked), 1)
	operatorEqual(t, "agent tool view retains foreign ID", response.ReviewsAsked[0].ReviewID, "reviewer-local-id")
	operatorEqual(t, "agent tool view retains requester ID", response.ReviewsAsked[0].AskID, "ask")
	page := f.send(protocol.Message{Type: protocol.TypeOperatorReview, ReviewOp: protocol.ReviewOpAsked})
	if page.ReviewAsked == nil || len(page.ReviewAsked.Items) != 1 {
		t.Fatal("operator asked page lost request")
	}
	operatorEqual(t, "operator page reports foreign review", page.ReviewAsked.Items[0].ReviewID, "reviewer-local-id")
}

func TestRemoteReviewIDsServerAcceptanceUnknownIDIsOmittedOnWire(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	operatorEqual(t, "pre-ID notice remains unknown", f.notice.ReviewOutcome.ReviewID, "")
	f.agent.write(protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "unknown"})
	r := f.agent.read()
	operatorEqual(t, "unknown view succeeds", r.Type, protocol.TypeReviewsAskedResult)
	data, err := json.Marshal(r.ReviewsAsked)
	operatorNoError(t, err)
	if strings.Contains(string(data), `"review_id"`) {
		t.Fatalf("unknown ID should be absent, not invented: %s", data)
	}
}

func TestRemoteReviewIDsServerAcceptanceLivePeerCarriesActualReviewerID(t *testing.T) {
	pair := newInstantPair(t)
	sides := pair.sides()
	sender, receiver := sides[0], sides[1]
	asker, reviewer := sender.f.peer("asker", protocol.Version), receiver.f.peer("reviewer", protocol.Version)
	sender.shareRemoteAgents(t, "asker")
	g := receiver.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpReviewGrant, ContactKey: receiver.key, ContactAgents: []string{"reviewer"}, ContactReviewGrant: &protocol.ContactReviewGrant{Mode: "auto"}, MessageID: "grant"})
	operatorEqual(t, "remote review allowed", g.Type, protocol.TypeOperatorContactResult)
	asker.write(protocol.Message{Type: protocol.TypeMsg, MessageID: "caller-ask", Workspace: sender.key, To: "reviewer", ReviewAsk: &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "259"}, Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}, Durable: true, AckRequested: true})
	operatorEqual(t, "request queued", asker.read().Stage, protocol.StageAccepted)
	notice := reviewer.read()
	if notice.ReviewID == "" {
		t.Fatal("reviewer notice has no local review ID")
	}
	outcome := awaitOutcomeState(t, asker, "requested")
	operatorEqual(t, "actual local reviewer ID reaches requester", outcome.ReviewOutcome.ReviewID, notice.ReviewID)
	operatorEqual(t, "requester identity remains distinct", outcome.ReviewOutcome.AskID, "caller-ask")
}
