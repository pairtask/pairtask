package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func refusalQueueAsk(t *testing.T, f workspaceStoreFixture, id string) {
	t.Helper()
	refusalQueueAs(t, f, id, "sender")
}

func refusalQueueAs(t *testing.T, f workspaceStoreFixture, id, agent string) {
	t.Helper()
	w := f.request(id)
	w.Msg.From, w.SenderSession = agent, "session-"+agent
	w.Review = remoteAsk(id).Review
	seq, err := f.db.LastSeq(context.Background())
	pairOK(t, err)
	w.Msg.Seq = seq + 1
	_, err = f.db.QueueWorkspaceSend(context.Background(), w)
	pairOK(t, err)
}

func refusalRecord(t *testing.T, f workspaceStoreFixture, id string) {
	t.Helper()
	pairOK(t, f.db.PeerMessages().Refused(context.Background(), f.key, messaging.Refused{ID: id, Refusal: messaging.Refuse(messaging.ReasonNotShared)}))
	a, found, err := f.db.PeerMessages().AskOutFor(context.Background(), f.key, id)
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, a.State, "refused")
	pairEqual(t, a.Settled, true)
}

func refusalPending(t *testing.T, f workspaceStoreFixture) []store.RemoteImport {
	t.Helper()
	imports, err := f.db.PendingImports(context.Background(), 100)
	pairOK(t, err)
	var notices []store.RemoteImport
	for _, imp := range imports {
		if imp.Contact == f.key && strings.HasPrefix(imp.RemoteID, "refused·") {
			notices = append(notices, imp)
		}
	}
	return notices
}

func refusalImport(t *testing.T, f workspaceStoreFixture, imp store.RemoteImport) store.ImportResult {
	t.Helper()
	seq, err := f.db.LastSeq(context.Background())
	pairOK(t, err)
	imp.Sessions = map[string]string{"sender": "session-sender"}
	result, err := f.db.ImportRemote(context.Background(), imp, seq+1, time.Now())
	pairOK(t, err)
	pairEqual(t, result.State, store.ImportImported)
	return result
}

func TestRefusalNoticeAcceptancePeerRefusalStagesRequesterNotice(t *testing.T) {
	f := workspaceStore(t)
	refusalQueueAsk(t, f, "denied-review")
	refusalRecord(t, f, "denied-review")
	notices := refusalPending(t, f)
	pairEqual(t, len(notices), 1)
	imp := notices[0]
	pairEqual(t, imp.RemoteID, "refused·denied-review")
	pairEqual(t, imp.Kind, "outcome")
	pairEqual(t, imp.Sender, "target")
	pairEqual(t, imp.ReplyTo.ID, "denied-review")
	pairEqual(t, fmt.Sprint(imp.Recipients), "[sender]")
	for _, want := range []string{"not_shared", "/pair reviews target ask", "new request", "Don't reply"} {
		if !strings.Contains(imp.Text, want) {
			t.Fatalf("notice lacks %q: %s", want, imp.Text)
		}
	}
	result := refusalImport(t, f, imp)
	pairEqual(t, result.Msg.Origin, protocol.OriginRemote)
	if result.Msg.Remote == nil {
		t.Fatal("refusal notice lacks paired workspace metadata")
	}
	pairEqual(t, result.Msg.Remote.Contact, f.key)
	pairEqual(t, result.Msg.Remote.Sender, "target")
	var body map[string]string
	pairOK(t, json.Unmarshal(result.Msg.Payload, &body))
	pairEqual(t, body["text"], imp.Text)
}

func TestRefusalNoticeAcceptanceReplayStagesExactlyOneNotice(t *testing.T) {
	f := workspaceStore(t)
	refusalQueueAsk(t, f, "denied-review")
	refusalRecord(t, f, "denied-review")
	refusalRecord(t, f, "denied-review")
	notices := refusalPending(t, f)
	pairEqual(t, len(notices), 1)
	result := refusalImport(t, f, notices[0])
	refusalRecord(t, f, "denied-review")
	var count int
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM remote_imports WHERE contact=? AND remote_id='refused·denied-review'`, f.key).Scan(&count))
	pairEqual(t, count, 1)
	pairEqual(t, len(refusalPending(t, f)), 0)
	pairEqual(t, result.Msg.To, "sender")
}

func TestRefusalNoticeAcceptanceOperatorAndOrdinaryRequestsDoNotCreateAgentNotices(t *testing.T) {
	for _, operator := range []bool{false, true} {
		t.Run(fmt.Sprint(operator), func(t *testing.T) {
			f := workspaceStore(t)
			if operator {
				m := remoteAsk("operator-review")
				_, err := f.db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: m.ID, Text: m.Text, To: m.To, Review: m.Review, Now: messageAt})
				pairOK(t, err)
				pairOK(t, f.db.PeerMessages().Refused(context.Background(), f.key, messaging.Refused{ID: m.ID, Refusal: messaging.Refuse(messaging.ReasonNotShared)}))
			} else {
				w := f.request("ordinary")
				_, err := f.db.QueueWorkspaceSend(context.Background(), w)
				pairOK(t, err)
				pairOK(t, f.db.PeerMessages().Refused(context.Background(), f.key, messaging.Refused{ID: "ordinary", Refusal: messaging.Refuse(messaging.ReasonNotShared)}))
			}
			imports, err := f.db.PendingImports(context.Background(), 100)
			pairOK(t, err)
			pairEqual(t, len(imports), 0)
		})
	}
}

func TestRefusalNoticeAcceptanceOtherReasonDoesNotOfferUnrelatedGrantFix(t *testing.T) {
	f := workspaceStore(t)
	refusalQueueAsk(t, f, "invalid-ask")
	pairOK(t, f.db.PeerMessages().Refused(context.Background(), f.key, messaging.Refused{ID: "invalid-ask", Refusal: messaging.Refuse(messaging.ReasonInvalid)}))
	notices := refusalPending(t, f)
	pairEqual(t, len(notices), 1)
	if strings.Contains(notices[0].Text, "/pair reviews") {
		t.Fatal("non-permission refusal suggests changing review grants")
	}
	if !strings.Contains(notices[0].Text, "invalid") {
		t.Fatal("non-permission refusal lost its reason")
	}
}

func TestRefusalNoticeAcceptanceCapIsPerContactAndRequestingAgent(t *testing.T) {
	f := workspaceStore(t)
	refusalBatch(t, f, 1, 17)
	routingRegister(t, f.db, "other")
	_, err := f.db.PeerMessages().Share(context.Background(), f.key, []string{"other"}, messageAt)
	pairOK(t, err)
	w := f.request("other-agent-ask")
	w.Msg.From, w.SenderSession = "other", "session-other"
	w.Review = remoteAsk(w.Msg.MessageID).Review
	seq, err := f.db.LastSeq(context.Background())
	pairOK(t, err)
	w.Msg.Seq = seq + 1
	_, err = f.db.QueueWorkspaceSend(context.Background(), w)
	pairOK(t, err)
	refusalRecord(t, f, w.Msg.MessageID)
	second := f
	second.key = messageContact(t, f.db, 2)
	_, err = f.db.PeerMessages().Share(context.Background(), second.key, []string{"sender"}, messageAt)
	pairOK(t, err)
	refusalQueueAsk(t, second, "second-contact-ask")
	refusalRecord(t, second, "second-contact-ask")
	var count int
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM remote_imports WHERE contact=? AND remote_id LIKE 'refused·%' AND recipients=json_array('sender')`, f.key).Scan(&count))
	pairEqual(t, count, 16)
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM remote_imports WHERE contact=? AND remote_id LIKE 'refused·%' AND recipients=json_array('other')`, f.key).Scan(&count))
	pairEqual(t, count, 1)
	pairEqual(t, len(refusalPending(t, second)), 1)
}

func refusalFillInbox(t *testing.T, f workspaceStoreFixture) []string {
	t.Helper()
	var ids []string
	for i := 0; i < store.MaxPendingMessages; i++ {
		id := fmt.Sprintf("busy-%02d", i)
		seq, err := f.db.LastSeq(context.Background())
		pairOK(t, err)
		msg := protocol.Message{Type: protocol.TypeMsg, MessageID: id, Seq: seq + 1, TS: &messageAt, From: "other", To: "sender", SessionID: "other-session", Durable: true, AckRequested: true, Payload: json.RawMessage(`{"text":"busy"}`)}
		pairOK(t, f.db.PersistMessage(context.Background(), msg, "other-session", []protocol.Recipient{{AgentID: "sender", SessionID: "session-sender"}}))
		ids = append(ids, id)
	}
	return ids
}

func TestRefusalNoticeAcceptanceFullInboxNeverRollsBackPeerRefusal(t *testing.T) {
	f := workspaceStore(t)
	ids := refusalFillInbox(t, f)
	refusalQueueAsk(t, f, "denied-review")
	refusalRecord(t, f, "denied-review")
	notices := refusalPending(t, f)
	pairEqual(t, len(notices), 1)
	seq, err := f.db.LastSeq(context.Background())
	pairOK(t, err)
	_, err = f.db.ImportRemote(context.Background(), notices[0], seq+1, time.Now())
	if !errors.Is(err, store.ErrInboxFull) {
		t.Fatalf("full inbox failed to defer notice: %v", err)
	}
	pairEqual(t, len(refusalPending(t, f)), 1)
	refusalAck(t, f, ids[0])
	refusalImport(t, f, notices[0])
}

func refusalSummaryCounts(t *testing.T, f workspaceStoreFixture) (int, int) {
	t.Helper()
	var pending, sequence int
	pairOK(t, f.raw.QueryRow(`SELECT pending,emitted_seq FROM refusal_summaries WHERE contact=? AND agent='sender'`, f.key).Scan(&pending, &sequence))
	return pending, sequence
}

func refusalBatch(t *testing.T, f workspaceStoreFixture, from, to int) {
	t.Helper()
	for i := from; i <= to; i++ {
		id := fmt.Sprintf("denied-%02d", i)
		refusalQueueAsk(t, f, id)
		refusalRecord(t, f, id)
	}
}

func refusalSummary(t *testing.T, f workspaceStoreFixture) store.RemoteImport {
	t.Helper()
	imports, err := f.db.PendingImports(context.Background(), 100)
	pairOK(t, err)
	var found []store.RemoteImport
	for _, imp := range imports {
		if imp.Contact == f.key && strings.HasPrefix(imp.RemoteID, "refusals·") {
			found = append(found, imp)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected one in-flight refusal summary, got %d", len(found))
	}
	return found[0]
}

func TestRefusalNoticeAcceptanceFullInboxBoundsSequentialRefusals(t *testing.T) {
	f := workspaceStore(t)
	refusalFillInbox(t, f)
	refusalBatch(t, f, 1, 17)
	pairEqual(t, len(refusalPending(t, f)), 16)
	summary := refusalSummary(t, f)
	if !strings.Contains(summary.Text, "1 more review requests") {
		t.Fatalf("wrong initial overflow count: %s", summary.Text)
	}
	refusalBatch(t, f, 18, 20)
	pairEqual(t, len(refusalPending(t, f)), 16)
	again := refusalSummary(t, f)
	pairEqual(t, again.RemoteID, summary.RemoteID)
	pairEqual(t, again.Text, summary.Text)
	pending, sequence := refusalSummaryCounts(t, f)
	pairEqual(t, pending, 3)
	pairEqual(t, sequence, 1)
	var refused int
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM review_asks_out WHERE contact=? AND state='refused'`, f.key).Scan(&refused))
	pairEqual(t, refused, 20)
}

func TestRefusalNoticeAcceptanceImportedUnacknowledgedNoticesStillCountTowardCap(t *testing.T) {
	f := workspaceStore(t)
	for i := 1; i <= 16; i++ {
		refusalBatch(t, f, i, i)
		notices := refusalPending(t, f)
		pairEqual(t, len(notices), 1)
		refusalImport(t, f, notices[0])
	}
	refusalBatch(t, f, 17, 17)
	pairEqual(t, len(refusalPending(t, f)), 0)
	summary := refusalSummary(t, f)
	result := refusalImport(t, f, summary)
	if result.Msg.Remote == nil {
		t.Fatal("summary has no paired workspace identity")
	}
	pairEqual(t, result.Msg.Remote.Sender, "")
	if result.Msg.From == "target" {
		t.Fatal("summary impersonates the reviewer")
	}
	for _, want := range []string{"1 more review requests", "reviews_asked", fmt.Sprintf("%d days", int(store.AskRetention/(24*time.Hour)))} {
		if !strings.Contains(summary.Text, want) {
			t.Fatalf("summary lacks %q: %s", want, summary.Text)
		}
	}
	refusalBatch(t, f, 18, 20)
	pending, sequence := refusalSummaryCounts(t, f)
	pairEqual(t, pending, 3)
	pairEqual(t, sequence, 1)
	var persisted string
	pairOK(t, f.raw.QueryRow(`SELECT body FROM remote_imports WHERE contact=? AND remote_id=?`, f.key, summary.RemoteID).Scan(&persisted))
	pairEqual(t, persisted, summary.Text)
	var wire string
	pairOK(t, f.raw.QueryRow(`SELECT payload FROM messages WHERE seq=?`, result.Msg.Seq).Scan(&wire))
	var text map[string]string
	pairOK(t, json.Unmarshal([]byte(wire), &text))
	pairEqual(t, text["text"], summary.Text)
}

func refusalReopen(t *testing.T, f *workspaceStoreFixture) {
	t.Helper()
	var seq int
	var name, path string
	pairOK(t, f.raw.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path))
	pairOK(t, f.db.Close())
	db, err := store.Open(path)
	pairOK(t, err)
	t.Cleanup(func() { _ = db.Close() })
	f.db = db
}

func TestRefusalNoticeAcceptanceRestartPreservesPendingDeltaAndDedup(t *testing.T) {
	f := workspaceStore(t)
	refusalFillInbox(t, f)
	refusalBatch(t, f, 1, 20)
	first := refusalSummary(t, f)
	refusalReopen(t, &f)
	refusalRecord(t, f, "denied-20")
	pairEqual(t, len(refusalPending(t, f)), 16)
	pending, sequence := refusalSummaryCounts(t, f)
	pairEqual(t, pending, 3)
	pairEqual(t, sequence, 1)
	again := refusalSummary(t, f)
	pairEqual(t, again.RemoteID, first.RemoteID)
	pairEqual(t, again.Text, first.Text)
}

func TestRefusalNoticeAcceptanceUnacknowledgedIndividualProtectsAskRetention(t *testing.T) {
	f := workspaceStore(t)
	refusalQueueAsk(t, f, "retained-ask")
	refusalRecord(t, f, "retained-ask")
	notices := refusalPending(t, f)
	pairEqual(t, len(notices), 1)
	result := refusalImport(t, f, notices[0])
	future := time.Now().Add(store.AskRetention + time.Hour)
	pairOK(t, f.db.PeerMessages().Collect(context.Background(), future))
	_, found, err := f.db.PeerMessages().AskOutFor(context.Background(), f.key, "retained-ask")
	pairOK(t, err)
	pairEqual(t, found, true)
	refusalAck(t, f, result.Msg.MessageID)
	pairOK(t, f.db.PeerMessages().Collect(context.Background(), future))
	_, found, err = f.db.PeerMessages().AskOutFor(context.Background(), f.key, "retained-ask")
	pairOK(t, err)
	pairEqual(t, found, false)
}

func refusalAck(t *testing.T, f workspaceStoreFixture, id string) {
	t.Helper()
	for _, stage := range []string{protocol.StageAdapterReceived, protocol.StageAgentAcknowledged} {
		_, err := f.db.Acknowledge(context.Background(), store.Acknowledgment{MessageID: id, Recipient: protocol.Recipient{AgentID: "sender", SessionID: "session-sender"}, Stage: stage, ProtocolVersion: protocol.Version})
		pairOK(t, err)
	}
}

func refusalImportedSummary(t *testing.T, f workspaceStoreFixture) store.ImportResult {
	t.Helper()
	for i := 1; i <= 16; i++ {
		refusalBatch(t, f, i, i)
		notices := refusalPending(t, f)
		pairEqual(t, len(notices), 1)
		refusalImport(t, f, notices[0])
	}
	refusalBatch(t, f, 17, 17)
	return refusalImport(t, f, refusalSummary(t, f))
}

func TestRefusalNoticeAcceptanceSummaryDeltaWaitsForAcknowledgmentAndSurvivesRestart(t *testing.T) {
	f := workspaceStore(t)
	first := refusalImportedSummary(t, f)
	refusalBatch(t, f, 18, 20)
	emitted, err := refusalFlush(t, f)
	pairOK(t, err)
	pairEqual(t, emitted, 0)
	refusalReopen(t, &f)
	emitted, err = refusalFlush(t, f)
	pairOK(t, err)
	pairEqual(t, emitted, 0)
	refusalAck(t, f, first.Msg.MessageID)
	emitted, err = refusalFlush(t, f)
	pairOK(t, err)
	pairEqual(t, emitted, 1)
	second := refusalSummary(t, f)
	pairEqual(t, second.RemoteID, "refusals·sender·2")
	if !strings.Contains(second.Text, "3 more review requests") {
		t.Fatalf("summary did not emit exact pending delta: %s", second.Text)
	}
	pending, sequence := refusalSummaryCounts(t, f)
	pairEqual(t, pending, 0)
	pairEqual(t, sequence, 2)
	emitted, err = refusalFlush(t, f)
	pairOK(t, err)
	pairEqual(t, emitted, 0)
}

func TestRefusalNoticeAcceptanceSummaryWriteFailureKeepsDeltaAndSequenceForRetry(t *testing.T) {
	f := workspaceStore(t)
	first := refusalImportedSummary(t, f)
	refusalBatch(t, f, 18, 20)
	refusalAck(t, f, first.Msg.MessageID)
	_, err := f.raw.Exec(`CREATE TRIGGER fail_summary BEFORE INSERT ON remote_imports WHEN NEW.remote_id LIKE 'refusals·%' BEGIN SELECT RAISE(ABORT,'summary insert failed'); END`)
	pairOK(t, err)
	emitted, err := refusalFlush(t, f)
	if err == nil || emitted != 0 {
		t.Fatal("failed summary write claimed progress")
	}
	pending, sequence := refusalSummaryCounts(t, f)
	pairEqual(t, pending, 3)
	pairEqual(t, sequence, 1)
	_, err = f.raw.Exec(`DROP TRIGGER fail_summary`)
	pairOK(t, err)
	emitted, err = refusalFlush(t, f)
	pairOK(t, err)
	pairEqual(t, emitted, 1)
	pairEqual(t, refusalSummary(t, f).RemoteID, "refusals·sender·2")
}

func TestRefusalNoticeAcceptanceTerminalOutcomeAndPeerRefusalDoNotDuplicate(t *testing.T) {
	for _, outcomeFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(outcomeFirst), func(t *testing.T) {
			f := workspaceStore(t)
			refusalQueueAsk(t, f, "overlap")
			outcome := messaging.ReviewOutcome{AskID: "overlap", Reviewer: "target", Seq: 1, State: "declined", Reason: "not available"}
			if outcomeFirst {
				answer, err := f.db.PeerMessages().AcceptOutcome(context.Background(), f.key, outcome, time.Now())
				pairOK(t, err)
				pairEqual(t, answer.Stored, true)
			}
			pairOK(t, f.db.PeerMessages().Refused(context.Background(), f.key, messaging.Refused{ID: "overlap", Refusal: messaging.Refuse(messaging.ReasonNotShared)}))
			if !outcomeFirst {
				_, err := f.db.PeerMessages().AcceptOutcome(context.Background(), f.key, outcome, time.Now())
				pairOK(t, err)
			}
			imports, err := f.db.PendingImports(context.Background(), 100)
			pairOK(t, err)
			pairEqual(t, len(imports), 1)
			var wanted string
			if outcomeFirst {
				wanted = "declined"
			} else {
				wanted = "refused"
			}
			ask, found, err := f.db.PeerMessages().AskOutFor(context.Background(), f.key, "overlap")
			pairOK(t, err)
			pairEqual(t, found, true)
			pairEqual(t, ask.State, wanted)
		})
	}
}

func refusalFlush(t *testing.T, f workspaceStoreFixture) (int, error) {
	t.Helper()
	flusher, ok := any(f.db.PeerMessages()).(interface {
		FlushRefusalSummaries(context.Context, time.Time) (int, error)
	})
	if !ok {
		t.Fatal("refusal summary flush API is not implemented")
	}
	return flusher.FlushRefusalSummaries(context.Background(), time.Now())
}

func TestRefusalNoticeAcceptanceJSONEscapedAgentStillHasBoundedScope(t *testing.T) {
	f := workspaceStore(t)
	const agent = "<sender>"
	routingRegister(t, f.db, agent)
	_, err := f.db.PeerMessages().Share(context.Background(), f.key, []string{agent}, messageAt)
	pairOK(t, err)
	for i := 1; i <= 17; i++ {
		id := fmt.Sprintf("escaped-%02d", i)
		refusalQueueAs(t, f, id, agent)
		refusalRecord(t, f, id)
	}
	var count int
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM remote_imports WHERE contact=? AND remote_id LIKE 'refused·%' AND json_extract(recipients,'$[0]')=?`, f.key, agent).Scan(&count))
	pairEqual(t, count, 16)
	for i := 18; i <= 20; i++ {
		id := fmt.Sprintf("escaped-%02d", i)
		refusalQueueAs(t, f, id, agent)
		refusalRecord(t, f, id)
	}
	pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM remote_imports WHERE contact=? AND remote_id LIKE 'refusals·%' AND json_extract(recipients,'$[0]')=?`, f.key, agent).Scan(&count))
	pairEqual(t, count, 1)
	var pending int
	pairOK(t, f.raw.QueryRow(`SELECT pending FROM refusal_summaries WHERE contact=? AND agent=?`, f.key, agent).Scan(&pending))
	pairEqual(t, pending, 3)
}
