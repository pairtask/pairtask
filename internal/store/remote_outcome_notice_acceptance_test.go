package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func importedOutcomeNotice(t *testing.T) (workspaceStoreFixture, protocol.Message) {
	t.Helper()
	return importedOutcomeNoticeFor(t, "target")
}

func importedOutcomeNoticeFor(t *testing.T, reviewer string) (workspaceStoreFixture, protocol.Message) {
	t.Helper()
	f := workspaceStore(t)
	w := f.request("ask")
	w.Msg.To = reviewer
	w.Review = remoteAsk("ask").Review
	_, err := f.db.QueueWorkspaceSend(context.Background(), w)
	pairOK(t, err)
	o := messaging.ReviewOutcome{AskID: "ask", Reviewer: reviewer, Seq: 1, State: "requested"}
	a, err := f.db.PeerMessages().AcceptOutcome(context.Background(), f.key, o, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	imports, err := f.db.PendingImports(context.Background(), 10)
	pairOK(t, err)
	for _, imp := range imports {
		if imp.Kind != "outcome" {
			continue
		}
		got, err := f.db.ImportRemote(context.Background(), imp, 2, messageAt)
		pairOK(t, err)
		pairEqual(t, got.State, store.ImportImported)
		return f, got.Msg
	}
	t.Fatal("outcome notice was not staged")
	return f, protocol.Message{}
}

func outcomeStoreReply(notice protocol.Message) store.RemoteReply {
	return store.RemoteReply{Msg: protocol.Message{Type: protocol.TypeMsg, MessageID: "outcome-reply", Seq: 3, From: "sender", To: notice.From}, SenderSession: "session-sender", Parent: notice.MessageID, Text: "follow up", Now: messageAt}
}

func TestRemoteOutcomeNoticeAcceptancePermissionChecksPrecedeNoticeKind(t *testing.T) {
	for _, change := range []struct{ from, to string }{{"intruder", "target"}, {"sender", "someone-else"}} {
		t.Run(change.from+"-"+change.to, func(t *testing.T) {
			f, notice := importedOutcomeNotice(t)
			reply := outcomeStoreReply(notice)
			reply.Msg.From, reply.Msg.To = change.from, change.to
			_, err := f.db.QueueRemoteReply(context.Background(), reply)
			pairEqual(t, errors.Is(err, messaging.ErrNotRecipient), true)
			pairEqual(t, errors.Is(err, store.ErrOutcomeNotice), false)
		})
	}
}

func TestRemoteOutcomeNoticeAcceptanceUnimportedParentKeepsExistingError(t *testing.T) {
	f, notice := importedOutcomeNotice(t)
	_, err := f.raw.Exec(`UPDATE remote_imports SET state='pending' WHERE local_id=?`, notice.MessageID)
	pairOK(t, err)
	_, err = f.db.QueueRemoteReply(context.Background(), outcomeStoreReply(notice))
	pairEqual(t, errors.Is(err, store.ErrNoImport), true)
	pairEqual(t, errors.Is(err, store.ErrOutcomeNotice), false)
}

func TestRemoteOutcomeNoticeAcceptanceReplyGuidanceQuotesActualRoutingIdentity(t *testing.T) {
	reviewer := "reviewer/@\n\"name\""
	f, notice := importedOutcomeNoticeFor(t, reviewer)
	var body struct {
		Text string `json:"text"`
	}
	pairOK(t, json.Unmarshal(notice.Payload, &body))
	followUp := body.Text
	pairEqual(t, strings.Contains(strings.ToLower(followUp), "reply"), true)
	pairEqual(t, strings.Contains(strings.ToLower(followUp), "don't reply"), false)
	for _, part := range []string{strconv.Quote(notice.MessageID), strconv.Quote(reviewer), strconv.Quote(f.key)} {
		if !strings.Contains(followUp, part) {
			t.Fatalf("outcome guidance lacks escaped routing %q: %s", part, followUp)
		}
	}
	if strings.Contains(followUp, reviewer) {
		t.Fatal("reviewer control characters leaked into new guidance")
	}
	pairEqual(t, notice.From, reviewer)
	pairEqual(t, notice.To, "sender")
	pairEqual(t, notice.Origin, protocol.OriginRemote)
	pairEqual(t, notice.Durable, true)
	pairEqual(t, notice.AckRequested, true)
	pairEqual(t, notice.InReplyTo, "ask")
	if notice.Remote == nil || notice.ReviewOutcome == nil {
		t.Fatal("outcome notice lost provenance or lifecycle metadata")
	}
	pairEqual(t, notice.Remote.Contact, f.key)
	pairEqual(t, notice.Remote.Sender, reviewer)
	pairEqual(t, notice.ReviewOutcome.Reviewer, reviewer)
	pairEqual(t, notice.ReviewOutcome.AskID, "ask")
}
