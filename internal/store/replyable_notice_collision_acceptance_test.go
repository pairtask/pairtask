package store_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestReplyableNoticeAcceptanceCollisionGuidanceNamesDeliveredNotice(t *testing.T) {
	f, staged := replyableStageOutcome(t)
	// Peer-provided summary text can quote the predictable staged ID before
	// the generated follow-up. Repair only the guidance, never peer data.
	peerText := "Peer quoted " + strconv.Quote(staged.LocalID) + ". "
	_, err := f.raw.Exec(`UPDATE remote_imports SET body=? WHERE local_id=?`, peerText+staged.Text, staged.LocalID)
	pairOK(t, err)
	(routingLocalSender{f.db, "sender"}).persist(t, staged.LocalID, 2)
	result, err := f.db.ImportRemote(context.Background(), staged, 3, messageAt)
	pairOK(t, err)
	pairEqual(t, result.State, store.ImportImported)
	if result.Msg.MessageID == staged.LocalID {
		t.Fatal("outcome notice reused an occupied local message ID")
	}
	var body struct{ Text string }
	pairOK(t, json.Unmarshal(result.Msg.Payload, &body))
	pairEqual(t, strings.HasPrefix(body.Text, peerText), true)
	replyableCollisionGuidance(t, body.Text, result.Msg.MessageID, staged.LocalID)
	reply := outcomeStoreReply(result.Msg)
	reply.Msg.Seq = 4
	reply.Msg.TS = &messageAt
	queued, err := f.db.QueueRemoteReply(context.Background(), reply)
	pairOK(t, err)
	pairEqual(t, queued.Row.ReplyTo.ID, "ask")
	pairEqual(t, queued.Row.ReplyTo.Author, messaging.ReplyBySender)
}

func replyableCollisionGuidance(t *testing.T, text, delivered, staged string) {
	t.Helper()
	_, guidance, found := strings.Cut(text, "To follow up, reply to this notice")
	pairEqual(t, found, true)
	if !strings.Contains(guidance, strconv.Quote(delivered)) {
		t.Fatalf("guidance names stale staged identity instead of delivered notice: %s", guidance)
	}
	if strings.Contains(guidance, strconv.Quote(staged)) {
		t.Fatal("reply guidance still quotes the unrelated local message's ID")
	}
}

func replyableStageOutcome(t *testing.T) (workspaceStoreFixture, store.RemoteImport) {
	t.Helper()
	f := workspaceStore(t)
	w := f.request("ask")
	w.Review = remoteAsk("ask").Review
	_, err := f.db.QueueWorkspaceSend(context.Background(), w)
	pairOK(t, err)
	_, err = f.db.PeerMessages().AcceptOutcome(context.Background(), f.key,
		messaging.ReviewOutcome{AskID: "ask", Reviewer: "target", Seq: 1, State: "requested"}, messageAt)
	pairOK(t, err)
	imports, err := f.db.PendingImports(context.Background(), 10)
	pairOK(t, err)
	for _, imp := range imports {
		if strings.HasPrefix(imp.RemoteID, "outcome·") {
			return f, imp
		}
	}
	t.Fatal("lifecycle outcome notice was not staged")
	return f, store.RemoteImport{}
}
