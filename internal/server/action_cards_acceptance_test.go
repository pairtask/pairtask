//go:build linux

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

type actionReceiptFrames struct{ input [][]byte }

func TestActionCardsServerAcceptanceTargetedAskReadsFindOlderRowsInRequestOrder(t *testing.T) {
	f := newReviewSignalFixture(t)
	for i := 1; i <= 35; i++ {
		m := reviewSignalAsk(fmt.Sprintf("card-ask-%02d", i), uint64(i), time.Now())
		_, err := f.st.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: m.ID, Text: m.Text, To: m.To, Review: m.Review, Now: m.CreatedAt})
		operatorNoError(t, err)
		operatorNoError(t, f.st.PeerMessages().Refused(context.Background(), f.key, messaging.Refused{ID: m.ID, Refusal: messaging.Refuse(messaging.ReasonNotShared)}))
	}
	refs := []map[string]string{{"contact": f.key, "ask_id": "card-ask-02"}, {"contact": f.key, "ask_id": "missing"}, {"contact": f.key, "ask_id": "card-ask-01"}, {"contact": f.key, "ask_id": "card-ask-02"}}
	r := reviewPendingPage(f, map[string]any{"review_op": "asked", "review_ask_refs": refs})
	operatorEqual(t, "targeted read succeeds", r.text("type"), protocol.TypeOperatorReviewResult)
	page := sessionDecode[protocol.ReviewAskedPage](t, r, "review_asked")
	operatorEqual(t, "only requested found rows counted", page.Total, 2)
	operatorEqual(t, "duplicates omitted", len(page.Items), 2)
	operatorEqual(t, "request order first", page.Items[0].AskID, "card-ask-02")
	operatorEqual(t, "older row remains reachable", page.Items[1].AskID, "card-ask-01")
	operatorEqual(t, "targeted read preserves refusal reason", page.Items[0].Reason, "not_shared")
	operatorEqual(t, "targeted result never paginates", page.More, false)
	operatorEqual(t, "targeted result has no cursor", page.Next, "")
	ordinary := reviewPendingPage(f, map[string]any{"review_op": "asked"})
	listed := sessionDecode[protocol.ReviewAskedPage](t, ordinary, "review_asked")
	operatorEqual(t, "ordinary asked page also preserves refusal reason", listed.Items[0].Reason, "not_shared")
}

func TestActionCardsServerAcceptanceTargetedAskReadsRejectMalformedSelectors(t *testing.T) {
	key := "sha256:" + strings.Repeat("a", 64)
	ref := map[string]string{"contact": key, "ask_id": "ask"}
	tooMany := make([]map[string]string, 9)
	for i := range tooMany {
		tooMany[i] = ref
	}
	for name, fields := range map[string]map[string]any{
		"empty refs":        {"review_ask_refs": []map[string]string{}},
		"blank contact":     {"review_ask_refs": []map[string]string{{"contact": "", "ask_id": "ask"}}},
		"blank ask":         {"review_ask_refs": []map[string]string{{"contact": key, "ask_id": ""}}},
		"malformed contact": {"review_ask_refs": []map[string]string{{"contact": "friendly alias", "ask_id": "ask"}}},
		"nine duplicates":   {"review_ask_refs": tooMany},
		"cursor and refs":   {"review_ask_refs": []map[string]string{ref}, "review_before": "anything"},
		"wrong operation":   {"review_op": "awaiting", "review_ask_refs": []map[string]string{ref}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newReviewSignalFixture(t)
			if fields["review_op"] == nil {
				fields["review_op"] = "asked"
			}
			r := reviewPendingPage(f, fields)
			operatorEqual(t, "invalid targeted read rejected", r.text("code"), protocol.ErrMalformedFrame)
			operatorEqual(t, "read never creates work", f.count("peer_messages"), 0)
		})
	}
}

func TestActionCardsServerAcceptanceTargetedAskReadsAreContactScoped(t *testing.T) {
	f := newReviewSignalFixture(t)
	m := reviewSignalAsk("same-ask-id", 1, time.Now())
	_, err := f.st.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: m.ID, Text: m.Text, To: m.To, Review: m.Review, Now: m.CreatedAt})
	operatorNoError(t, err)
	other := "sha256:" + strings.Repeat("b", 64)
	if other == f.key {
		other = "sha256:" + strings.Repeat("c", 64)
	}
	r := reviewPendingPage(f, map[string]any{"review_op": "asked", "review_ask_refs": []map[string]string{{"contact": other, "ask_id": m.ID}}})
	operatorEqual(t, "unknown scope read succeeds", r.text("type"), protocol.TypeOperatorReviewResult)
	page := sessionDecode[protocol.ReviewAskedPage](t, r, "review_asked")
	operatorEqual(t, "same ID in another contact never matches", len(page.Items), 0)
	operatorEqual(t, "scoped total", page.Total, 0)
}

func (f *actionReceiptFrames) Send([]byte) error { return nil }
func (f *actionReceiptFrames) Recv() ([]byte, error) {
	if len(f.input) == 0 {
		return nil, io.EOF
	}
	frame := f.input[0]
	f.input = f.input[1:]
	return frame, nil
}

func actionAwaitRefusal(t *testing.T, agent *operatorPeer) protocol.Message {
	t.Helper()
	for i := 0; i < 12; i++ {
		msg := agent.read()
		if msg.Type == protocol.TypeMsg && msg.Origin == protocol.OriginRemote {
			return msg
		}
	}
	t.Fatal("requesting agent never received a refusal notice")
	return protocol.Message{}
}

func TestActionCardsServerAcceptanceRequesterGetsRefusalAndCannotReplyToNotice(t *testing.T) {
	p := newInstantPair(t)
	sides := p.sides()
	sender, receiver := sides[0], sides[1]
	asker := sender.f.peer("requester", protocol.Version)
	receiver.f.peer("reviewer", protocol.Version)
	sender.shareRemoteAgents(t, "requester")
	asker.write(protocol.Message{Type: protocol.TypeMsg, MessageID: "review-refused", Workspace: sender.key, To: "reviewer", Durable: true, AckRequested: true, Payload: json.RawMessage(`{"text":"review this"}`), ReviewAsk: &protocol.ReviewAsk{ReviewTarget: protocol.ReviewTarget{Forge: "github", Repository: "pairtask/pairtask", Change: "302"}, Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40)}})
	operatorEqual(t, "request queued locally", asker.read().Stage, protocol.StageAccepted)
	notice := actionAwaitRefusal(t, asker)
	if notice.Remote == nil {
		t.Fatal("refusal notice lacks paired identity")
	}
	operatorEqual(t, "refusal names actual peer", notice.Remote.Contact, sender.key)
	operatorEqual(t, "refusal names asked reviewer", notice.Remote.Sender, "reviewer")
	operatorEqual(t, "notice is for requester", notice.To, "requester")
	var body map[string]string
	operatorNoError(t, json.Unmarshal(notice.Payload, &body))
	for _, want := range []string{"not_shared", "/pair reviews reviewer ask", "new request"} {
		if !strings.Contains(body["text"], want) {
			t.Fatalf("refusal notice lacks %q: %s", want, body["text"])
		}
	}
	operatorEqual(t, "refusal never creates reviewer work", receiver.f.count("reviews"), 0)
	acknowledgeRemoteAgent(t, asker, notice.MessageID)
	before := sender.f.count("peer_messages")
	reply := outcomeNoticeReply(notice.MessageID, sender.key)
	reply.To = "reviewer"
	asker.write(reply)
	response := asker.read()
	operatorEqual(t, "synthetic refusal notice reply rejected", response.Code, protocol.ErrMalformedFrame)
	operatorEqual(t, "synthetic notice gives follow-up guidance", response.Detail, outcomeReplyGuidance)
	operatorEqual(t, "notice reply never queued a wire message", sender.f.count("peer_messages"), before)
	p.assertOneConnection(t)
}

func actionReceiptScript(t *testing.T, receipt string) *actionReceiptFrames {
	t.Helper()
	hello, err := messaging.EncodeHello(messaging.Hello{Min: 1, Max: 3})
	operatorNoError(t, err)
	end, err := messaging.EncodeEnd()
	operatorNoError(t, err)
	directory, err := messaging.EncodeDirectory(nil)
	operatorNoError(t, err)
	f := &actionReceiptFrames{input: [][]byte{hello, end, end, end}}
	switch receipt {
	case "valid":
		f.input = append(f.input, end, directory)
	case "invalid":
		f.input = append(f.input, directory)
	case "lost":
	default:
		t.Fatal("unknown receipt fixture")
	}
	return f
}

func TestActionCardsServerAcceptanceDirectoryAcknowledgmentRequiresValidEnd(t *testing.T) {
	for _, receipt := range []string{"valid", "invalid", "lost"} {
		t.Run(receipt, func(t *testing.T) { actionReceiptRound(t, receipt) })
	}
}

func actionReceiptRound(t *testing.T, receipt string) {
	t.Helper()
	f := newRedeemOperator(t, redeemConfig(), "")
	identity := messageIdentity(t)
	messageConfirm(f, identity, 1)
	key := identity.Fingerprint()
	session := messaging.Session{Frames: actionReceiptScript(t, receipt), Store: f.st.PeerMessages(), Contact: key, Now: time.Now}
	result, _, err := session.OpenDial(context.Background())
	if receipt == "valid" {
		operatorNoError(t, err)
	} else if err == nil {
		t.Fatal("bad receipt unexpectedly completed round")
	}
	operatorEqual(t, "only decoded End confirms directory", result.DirectoryAcked, receipt == "valid")
	snapshot, err := f.st.PeerMessages().SharedSnapshot(context.Background(), key)
	operatorNoError(t, err)
	operatorEqual(t, "no receipt is not acknowledged empty set", snapshot.Confirmed(), receipt == "valid")
	if receipt == "valid" {
		session.Frames = actionReceiptScript(t, receipt)
		replayed, _, err := session.OpenDial(context.Background())
		operatorNoError(t, err)
		operatorEqual(t, "identical receipt is not progress", replayed.DirectoryAcked, false)
		operatorEqual(t, "unchanged receipt cannot cause refresh loop", replayed.Moved(), false)
	}
}

type actionFailedAcker struct{ *store.PeerMessages }

func (actionFailedAcker) DirectoryAcked(context.Context, string, []string, time.Time) (bool, error) {
	return true, io.ErrClosedPipe
}

func TestActionCardsServerAcceptanceFailedAckWriteCannotReportProgress(t *testing.T) {
	f := newRedeemOperator(t, redeemConfig(), "")
	identity := messageIdentity(t)
	messageConfirm(f, identity, 1)
	session := messaging.Session{Frames: actionReceiptScript(t, "valid"), Store: actionFailedAcker{f.st.PeerMessages()}, Contact: identity.Fingerprint(), Now: time.Now}
	result, _, err := session.OpenDial(context.Background())
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("acknowledgment failure lost: %v", err)
	}
	operatorEqual(t, "failed acknowledgment is not progress", result.DirectoryAcked, false)
	operatorEqual(t, "failed acknowledgment cannot trigger refresh", result.Moved(), false)
}

func actionAwaitDirectoryReceipt(t *testing.T, side instantSide, agents []string) store.SharedSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		snapshot, err := side.f.st.PeerMessages().SharedSnapshot(context.Background(), side.key)
		operatorNoError(t, err)
		if snapshot.Confirmed() && snapshot.Digest == store.DirectoryDigest(agents) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatal("live peer did not acknowledge current shared set")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestActionCardsServerAcceptanceLiveShareAndUnshareConfirmExactSet(t *testing.T) {
	p := newInstantPair(t)
	sides := p.sides()
	sender, receiver := sides[0], sides[1]
	sender.f.peer("sender", protocol.Version)
	sender.shareRemoteAgents(t, "sender")
	snapshot := actionAwaitDirectoryReceipt(t, sender, []string{"sender"})
	if snapshot.AckAt == nil {
		t.Fatal("confirmed directory has no receipt time")
	}
	row := sender.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpList, ContactKey: sender.key})
	if row.ContactPage == nil {
		t.Fatal("contact page missing")
	}
	operatorEqual(t, "public page reports acknowledged current set", row.ContactPage.SharedSync, protocol.SharedSyncConfirmed)
	operatorEqual(t, "public page binds receipt to shared digest", row.ContactPage.SharedAckDigest, row.ContactPage.SharedDigest)
	unshare := sender.f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpUnshare, ContactKey: sender.key, ContactAgents: []string{"sender"}, MessageID: "remove-share"})
	operatorEqual(t, "unshare applied locally", unshare.Type, protocol.TypeOperatorContactResult)
	actionAwaitDirectoryReceipt(t, sender, nil)
	directory, err := receiver.f.st.PeerMessages().PeerDirectory(context.Background(), receiver.key)
	operatorNoError(t, err)
	operatorEqual(t, "peer has empty set after unshare", len(directory), 0)
	p.assertOneConnection(t)
}
