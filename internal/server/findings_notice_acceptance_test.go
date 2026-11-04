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
	operatorEqual(t, "list reports received count", kept.Received, 5)
	operatorEqual(t, "list carries no finding text", len(kept.Items), 0)
	f.agent.write(protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "findings-detail", ReviewAskRefs: []protocol.ReviewAskRef{{Contact: f.key, AskID: "ask"}}})
	detail := f.agent.read()
	operatorEqual(t, "detail succeeds", detail.Type, protocol.TypeReviewsAskedResult)
	operatorEqual(t, "detail identifies one ask", len(detail.ReviewsAsked), 1)
	full := detail.ReviewsAsked[0].Findings
	if full == nil {
		t.Fatal("detail lost findings")
	}
	operatorEqual(t, "agent only reads items actually transferred", len(full.Items), 5)
	operatorEqual(t, "retrieval includes beyond notice preview", full.Items[4].Path, "src/issue-4.go")
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
	operatorEqual(t, "older agent sees transferred count", kept.Received, 1)
	operatorEqual(t, "older list stays bounded", len(kept.Items), 0)
}

func findingsBulkOutcome(t *testing.T) messaging.ReviewOutcome {
	t.Helper()
	full := make([]messaging.Finding, 24)
	for i := range full {
		full[i] = messaging.Finding{Path: strings.Repeat("<>&", 170), Line: i, Summary: strings.Repeat("<>&", 170)}
	}
	o := findingsServerOutcome(t, 100, 0)
	o.Findings = messaging.ProjectFindings(100, full)
	o.Summary = strings.Repeat("<>&", 1365)
	_, err := messaging.EncodeFindings(o.Findings)
	operatorNoError(t, err)
	return o
}

func findingsBulkServer(t *testing.T, count int) *outcomeNoticeServer {
	t.Helper()
	f := newOutcomeNoticeServer(t)
	o := findingsBulkOutcome(t)
	notice := findingsServerNotice(t, f, o)
	acknowledgeRemoteAgent(t, f.agent, notice.MessageID)
	findingsCloneSettledAsks(t, f, o, count)
	return f
}

func findingsCloneSettledAsks(t *testing.T, f *outcomeNoticeServer, o messaging.ReviewOutcome, count int) {
	t.Helper()
	// Start with a real queued/accepted anchor; clone retained settled asks whose
	// message/notice history may be pruned, recomputing both per-ask digests.
	tx, err := f.db.Begin()
	operatorNoError(t, err)
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO review_asks_out
(contact,ask_id,reviewer,requester_agent,local_id,ask,state,seq,digest,attempt,intent,verdict,summary,url,reason,review_id,created_at,updated_at,settled_at,findings,findings_seq,findings_attempt,findings_intent,findings_digest)
SELECT contact,?,reviewer,requester_agent,?,ask,state,seq,?,attempt,intent,verdict,summary,url,reason,review_id,created_at,updated_at,settled_at,findings,findings_seq,findings_attempt,findings_intent,?
FROM review_asks_out WHERE contact=? AND ask_id='ask'`)
	operatorNoError(t, err)
	defer stmt.Close()
	for i := 1; i < count; i++ {
		o.AskID = fmt.Sprintf("bulk-%03d", i)
		sum := o.Digest()
		_, err = stmt.Exec(o.AskID, o.AskID, sum[:], sum[:], f.key)
		operatorNoError(t, err)
	}
	operatorNoError(t, tx.Commit())
}

func findingsReadAgentPage(t *testing.T, f *outcomeNoticeServer, before string) protocol.Message {
	t.Helper()
	f.agent.write(protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "bulk-findings", ReviewBefore: before})
	operatorNoError(t, f.agent.SetReadDeadline(time.Now().Add(4*time.Second)))
	line, err := f.agent.r.ReadBytes('\n')
	operatorNoError(t, err)
	operatorEqual(t, "actual encoded list is a bounded frame", len(line) <= protocol.MaxFrameBytes, true)
	var response protocol.Message
	operatorNoError(t, json.Unmarshal(line, &response))
	operatorEqual(t, "large findings list remains retrievable", response.Type, protocol.TypeReviewsAskedResult)
	return response
}

func findingsCheckPage(t *testing.T, page protocol.Message, seen map[string]bool, previous string) string {
	t.Helper()
	operatorEqual(t, "nonempty data yields a useful page", len(page.ReviewsAsked) > 0, true)
	operatorEqual(t, "page has at most 64 asks", len(page.ReviewsAsked) <= 64, true)
	for _, ask := range page.ReviewsAsked {
		if seen[ask.AskID] {
			t.Fatalf("paging repeated %q", ask.AskID)
		}
		if previous != "" {
			operatorEqual(t, "equal-time asks are descending", previous > ask.AskID, true)
		}
		seen[ask.AskID], previous = true, ask.AskID
		if ask.Findings == nil {
			t.Fatal("counts-only list lost known findings")
		}
		operatorEqual(t, "list omits finding text", len(ask.Findings.Items), 0)
	}
	return previous
}

func TestFindingsNoticeAcceptanceWorstCaseAgentListFitsActualWireFrame(t *testing.T) {
	f := findingsBulkServer(t, 128)
	seen, cursors := map[string]bool{}, map[string]bool{}
	before, previous := "", ""
	for pages := 0; pages < 128; pages++ {
		page := findingsReadAgentPage(t, f, before)
		previous = findingsCheckPage(t, page, seen, previous)
		if !page.ReviewsAskedMore {
			operatorEqual(t, "every retained ask is reachable", len(seen), 128)
			return
		}
		before = page.ReviewsAskedNext
		if before == "" {
			t.Fatal("more flag has no continuation")
		}
		if cursors[before] {
			t.Fatal("paging repeated its cursor")
		}
		cursors[before] = true
	}
	t.Fatal("paging never terminated")
}

func TestFindingsNoticeAcceptanceWorstCaseOperatorPageFitsWireFrame(t *testing.T) {
	f := findingsBulkServer(t, 64)
	owner := sessionOpen(f.operatorFixture)
	owner.snapshot()
	response, _ := owner.request(protocol.TypeOperatorReview, map[string]any{"review_op": "asked"})
	operatorEqual(t, "operator page remains retrievable", response.text("type"), protocol.TypeOperatorReviewResult)
	page := sessionDecode[protocol.ReviewAskedPage](t, response, "review_asked")
	operatorEqual(t, "operator page still counts all retained requests", page.Total, 64)
	operatorEqual(t, "operator page remains useful", len(page.Items) > 0, true)
}

func TestFindingsNoticeAcceptanceCardBatchIsPreviewNotFullTransfer(t *testing.T) {
	f := findingsBulkServer(t, 64)
	refs := []map[string]string{{"contact": f.key, "ask_id": "ask"}}
	for i := 1; i < 8; i++ {
		refs = append(refs, map[string]string{"contact": f.key, "ask_id": fmt.Sprintf("bulk-%03d", i)})
	}
	owner := sessionOpen(f.operatorFixture)
	owner.snapshot()
	response, _ := owner.request(protocol.TypeOperatorReview, map[string]any{"review_op": "asked", "review_ask_refs": refs})
	operatorEqual(t, "eight-ref preview remains retrievable", response.text("type"), protocol.TypeOperatorReviewResult)
	page := sessionDecode[protocol.ReviewAskedPage](t, response, "review_asked")
	operatorEqual(t, "all eight requested cards returned", len(page.Items), 8)
	for _, item := range page.Items {
		if item.Findings == nil {
			t.Fatal("preview lost known findings counts")
		}
		operatorEqual(t, "card preview bounds items", len(item.Findings.Items) <= 3, true)
	}
}

func TestFindingsNoticeAcceptanceDetailIsSingleAskAndRequesterOwned(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	notice := findingsServerNotice(t, f, findingsServerOutcome(t, 9, 5))
	acknowledgeRemoteAgent(t, f.agent, notice.MessageID)
	ref := protocol.ReviewAskRef{Contact: f.key, AskID: "ask"}
	other := f.peer("other-reader", protocol.Version)
	other.write(protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "not-my-ask", ReviewAskRefs: []protocol.ReviewAskRef{ref}})
	answer := other.read()
	operatorEqual(t, "another agent's read is well formed", answer.Type, protocol.TypeReviewsAskedResult)
	operatorEqual(t, "another agent cannot read requester findings", len(answer.ReviewsAsked), 0)
	owner := sessionOpen(f.operatorFixture)
	owner.snapshot()
	r, _ := owner.request(protocol.TypeOperatorReview, map[string]any{"review_op": "asked_detail", "review_ask_refs": []protocol.ReviewAskRef{ref}})
	operatorEqual(t, "operator detail succeeds", r.text("type"), protocol.TypeOperatorReviewResult)
	page := sessionDecode[protocol.ReviewAskedPage](t, r, "review_asked")
	operatorEqual(t, "detail identifies one ask", len(page.Items), 1)
	got := page.Items[0].Findings
	if got == nil {
		t.Fatal("operator detail lost findings")
	}
	operatorEqual(t, "detail retains actual received count", got.Received, 5)
	operatorEqual(t, "detail carries all received items", len(got.Items), 5)
	operatorEqual(t, "detail reads fourth finding", got.Items[3].Path, "src/issue-3.go")
}

func TestFindingsNoticeAcceptanceDetailRejectsAmbiguousSelectors(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	owner := sessionOpen(f.operatorFixture)
	owner.snapshot()
	ref := protocol.ReviewAskRef{Contact: f.key, AskID: "ask"}
	for _, refs := range [][]protocol.ReviewAskRef{nil, {ref, ref}, {{Contact: "Office", AskID: "ask"}}} {
		r, _ := owner.request(protocol.TypeOperatorReview, map[string]any{"review_op": "asked_detail", "review_ask_refs": refs})
		operatorEqual(t, "detail refuses missing, multiple or noncanonical identity", r.text("code"), "malformed_frame")
	}
	for _, refs := range [][]protocol.ReviewAskRef{{ref, ref}, {{Contact: "Office", AskID: "ask"}}} {
		f.agent.write(protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "bad-detail", ReviewAskRefs: refs})
		operatorEqual(t, "agent detail refuses ambiguous identity", f.agent.read().Code, "malformed_frame")
	}
	f.agent.write(protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "bad-cursor", ReviewBefore: "not-an-issued-cursor"})
	operatorEqual(t, "agent refuses malformed cursor", f.agent.read().Code, "malformed_frame")
	f.agent.write(protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "mixed-detail", ReviewBefore: "not-an-issued-cursor", ReviewAskRefs: []protocol.ReviewAskRef{ref}})
	operatorEqual(t, "agent refuses detail combined with cursor", f.agent.read().Code, "malformed_frame")
	r, _ := owner.request(protocol.TypeOperatorReview, map[string]any{"review_op": "asked_detail", "review_ask_refs": []protocol.ReviewAskRef{ref}, "review_before": "cursor"})
	operatorEqual(t, "operator refuses detail combined with cursor", r.text("code"), "malformed_frame")
}

func findingsSecondContactAsk(t *testing.T, f *outcomeNoticeServer) string {
	t.Helper()
	id := messageIdentity(t)
	messageConfirm(f.redeemOperatorFixture, id, 2)
	key := id.Fingerprint()
	// Retained anchors may outlive their original chat rows. Give the second
	// contact the same contact-scoped ask ID with independently retained data.
	data, err := messaging.EncodeFindings(&messaging.FindingsEnvelope{Total: 1, Items: []messaging.Finding{{Path: "second-contact.go", Line: 4, Summary: "second contact finding"}}})
	operatorNoError(t, err)
	_, err = f.db.Exec(`INSERT INTO review_asks_out
(contact,ask_id,reviewer,requester_agent,local_id,ask,state,seq,digest,attempt,intent,verdict,summary,url,reason,review_id,created_at,updated_at,settled_at,findings,findings_seq,findings_attempt,findings_intent,findings_digest)
SELECT ?,ask_id,reviewer,requester_agent,'second-retained-local-id',ask,state,seq,digest,attempt,intent,verdict,summary,url,reason,review_id,created_at,updated_at,settled_at,?,findings_seq,findings_attempt,findings_intent,findings_digest
FROM review_asks_out WHERE contact=? AND ask_id='ask'`, key, string(data), f.key)
	operatorNoError(t, err)
	return key
}

func findingsListIdentity(t *testing.T, row protocol.ReviewOutcomeView) protocol.ReviewAskRef {
	t.Helper()
	data, err := json.Marshal(row)
	operatorNoError(t, err)
	var selector struct {
		Workspace string `json:"workspace"`
		AskID     string `json:"ask_id"`
	}
	operatorNoError(t, json.Unmarshal(data, &selector))
	if selector.Workspace == "" {
		t.Fatal("agent list row omits the workspace required to read its detail")
	}
	return protocol.ReviewAskRef{Contact: selector.Workspace, AskID: selector.AskID}
}

func TestFindingsNoticeAcceptanceListDiscoversDetailAcrossContactScopedAskIDs(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	notice := findingsServerNotice(t, f, findingsServerOutcome(t, 9, 5))
	acknowledgeRemoteAgent(t, f.agent, notice.MessageID)
	second := findingsSecondContactAsk(t, f)
	want := map[string]string{f.key: "src/issue-0.go", second: "second-contact.go"}
	f.agent.write(protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "discover-detail"})
	list := f.agent.read()
	operatorEqual(t, "both contact-scoped asks are listed", len(list.ReviewsAsked), 2)
	for _, row := range list.ReviewsAsked {
		ref := findingsListIdentity(t, row) // no caller-supplied workspace fallback
		operatorEqual(t, "both contacts may use the same ask ID", ref.AskID, "ask")
		path, known := want[ref.Contact]
		operatorEqual(t, "list supplies canonical retained contact", known, true)
		f.agent.write(protocol.Message{Type: protocol.TypeReviewsAskedGet, RequestID: "discovered-detail", ReviewAskRefs: []protocol.ReviewAskRef{ref}})
		answer := f.agent.read()
		operatorEqual(t, "discovered selector reads one detail", len(answer.ReviewsAsked), 1)
		got := answer.ReviewsAsked[0].Findings
		if got == nil {
			t.Fatal("discovered detail lost findings")
		}
		operatorEqual(t, "list selector disambiguates correct contact's data", got.Items[0].Path, path)
		delete(want, ref.Contact)
	}
	operatorEqual(t, "both contacts were discoverable", len(want), 0)
}
