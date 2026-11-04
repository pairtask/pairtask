//go:build linux

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
)

func findingsServerOutcome(t *testing.T, total, items int) messaging.ReviewOutcome {
	t.Helper()
	envelope := &messaging.FindingsEnvelope{Total: total, Items: []messaging.Finding{}}
	for i := 0; i < items; i++ {
		envelope.Items = append(envelope.Items, messaging.Finding{Path: fmt.Sprintf("src/issue-%d.go", i), Line: i + 1, Summary: fmt.Sprintf("finding-%d", i)})
	}
	return messaging.ReviewOutcome{AskID: "ask", Reviewer: "target", Seq: 2, Attempt: 1, Intent: 1, State: "published", Verdict: "comment", Summary: "reviewed", URL: "https://example.test/review/303", Findings: envelope}
}

func findingsServerNotice(t *testing.T, f *outcomeNoticeServer, o messaging.ReviewOutcome) protocol.Message {
	t.Helper()
	a, err := f.st.PeerMessages().AcceptOutcome(context.Background(), f.key, o, time.Now())
	operatorNoError(t, err)
	operatorEqual(t, "findings outcome accepted", a.Stored, true)
	f.rescan("import-findings")
	notice := f.agent.read()
	operatorEqual(t, "findings remain paired data", notice.Origin, protocol.OriginRemote)
	if notice.Remote == nil {
		t.Fatal("findings notice has no remote provenance")
	}
	operatorEqual(t, "findings notice contact", notice.Remote.Contact, f.key)
	return notice
}

func TestFindingsNoticeAcceptanceSeparatesReceivedAndNotTransferredCounts(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	notice := findingsServerNotice(t, f, findingsServerOutcome(t, 9, 5))
	var body struct {
		Text string `json:"text"`
	}
	operatorNoError(t, json.Unmarshal(notice.Payload, &body))
	for _, want := range []string{"src/issue-0.go:1: finding-0", "src/issue-1.go:2: finding-1", "src/issue-2.go:3: finding-2", "2 more received: reviews_asked", "4 not transferred, kept on their side", "https://example.test/review/303"} {
		if !strings.Contains(body.Text, want) {
			t.Fatalf("findings notice lacks %q: %s", want, body.Text)
		}
	}
	if strings.Contains(body.Text, "src/issue-3.go") {
		t.Fatal("notice expanded beyond first three findings")
	}
	if strings.Contains(body.Text, "6 more received") {
		t.Fatal("notice claims untransferred findings are in reviews_asked")
	}
}

func TestFindingsNoticeAcceptanceEmptyVerdictNeverClaimsUntransferredFindings(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	notice := findingsServerNotice(t, f, findingsServerOutcome(t, 0, 0))
	var body struct {
		Text string `json:"text"`
	}
	operatorNoError(t, json.Unmarshal(notice.Payload, &body))
	if strings.Contains(body.Text, "not transferred") {
		t.Fatal("empty verdict falsely implies withheld findings")
	}
	kept := findingsAgentSnapshot(t, f, notice)
	operatorEqual(t, "known-empty findings remain available", kept != nil, true)
	operatorEqual(t, "known-empty total", kept.Total, 0)
	operatorEqual(t, "known-empty items", len(kept.Items), 0)
}

func findingsAgentSnapshot(t *testing.T, f *outcomeNoticeServer, notice protocol.Message) *protocol.AskFindings {
	t.Helper()
	acknowledgeRemoteAgent(t, f.agent, notice.MessageID)
	f.agent.write(protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "findings-list"})
	response := f.agent.read()
	operatorEqual(t, "agent findings query succeeds", response.Type, protocol.TypeReviewsAskedResult)
	operatorEqual(t, "only requester's own request returned", len(response.ReviewsAsked), 1)
	data, err := json.Marshal(response.ReviewsAsked[0])
	operatorNoError(t, err)
	var view struct {
		Findings *protocol.AskFindings `json:"findings"`
	}
	operatorNoError(t, json.Unmarshal(data, &view))
	if view.Findings == nil {
		t.Fatal("known findings missing from requesting agent's view")
	}
	return view.Findings
}

func TestFindingsNoticeAcceptanceAgentCanRetrieveOnlyTransferredItems(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	notice := findingsServerNotice(t, f, findingsServerOutcome(t, 9, 5))
	kept := findingsAgentSnapshot(t, f, notice)
	operatorEqual(t, "agent sees full verdict count", kept.Total, 9)
	operatorEqual(t, "agent only claims items actually transferred", len(kept.Items), 5)
	operatorEqual(t, "retrieval includes beyond notice preview", kept.Items[4].Path, "src/issue-4.go")
	operatorEqual(t, "snapshot outcome attribution", kept.Seq, uint64(2))
	operatorEqual(t, "snapshot attempt attribution", kept.Attempt, uint32(1))
}

func findingsOlderAgent(t *testing.T) *outcomeNoticeServer {
	t.Helper()
	f := newRedeemOperator(t, redeemConfig(), "")
	id := messageIdentity(t)
	messageConfirm(f, id, 1)
	n := &outcomeNoticeServer{redeemOperatorFixture: f, key: id.Fingerprint(), agent: f.peer("sender", protocol.ReviewFindingsVersion-1)}
	n.rescan("share")
	n.agent.write(protocol.Message{Type: protocol.TypeMsg, MessageID: "ask", Workspace: n.key, To: "target", Payload: json.RawMessage(`{"text":"review"}`), ReviewAsk: &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "303"}, Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}, Durable: true, AckRequested: true})
	operatorEqual(t, "older agent request accepted", n.agent.read().Stage, protocol.StageAccepted)
	return n
}

func TestFindingsNoticeAcceptanceAgentFieldIsAdditiveForPreviousVersion(t *testing.T) {
	f := findingsOlderAgent(t)
	notice := findingsServerNotice(t, f, findingsServerOutcome(t, 1, 1))
	kept := findingsAgentSnapshot(t, f, notice)
	operatorEqual(t, "additive field is available to older agent", kept.Total, 1)
	operatorEqual(t, "older agent sees actual transferred item", kept.Items[0].Path, "src/issue-0.go")
}
