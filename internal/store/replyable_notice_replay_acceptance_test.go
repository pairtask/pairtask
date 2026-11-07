package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func TestReplyableNoticeAcceptanceRetiredNoticeCannotCrossRepairedPairing(t *testing.T) {
	for _, kind := range []string{"request", "outcome"} {
		f, notice := replyableStoredNotice(t, kind)
		namespaceRepair(t, f)
		parent := notice.MessageID
		if kind == "request" {
			parent = notice.ReviewID
		}
		before := wireMutationSnapshot(t, f)
		_, err := f.db.QueueRemoteReply(context.Background(), store.RemoteReply{Msg: protocol.Message{Type: protocol.TypeMsg,
			MessageID: "retired-notice-reply", Seq: 100, TS: &messageAt, From: notice.To, To: notice.From,
			InReplyTo: parent, Payload: []byte(`{"text":"follow-up"}`)}, Parent: parent, Text: "follow-up", Now: messageAt})
		pairEqual(t, errors.Is(err, store.ErrUnpaired), true)
		if !reflect.DeepEqual(before, wireMutationSnapshot(t, f)) {
			t.Fatal("retired notice reply changed history, outbox, audit or sequence state")
		}
	}
}

func TestReplyableNoticeAcceptanceProvenanceSurvivesStoreRestart(t *testing.T) {
	for _, kind := range []string{"request", "outcome"} {
		t.Run(kind, func(t *testing.T) {
			f, notice := replyableStoredNotice(t, kind)
			var path string
			pairOK(t, f.raw.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path))
			pairOK(t, f.db.Close())
			reopened, err := store.Open(path)
			pairOK(t, err)
			defer reopened.Close()
			messages, err := reopened.ClaimPendingFor(context.Background(), protocol.Recipient{AgentID: notice.To, SessionID: "replacement"}, protocol.Version)
			pairOK(t, err)
			replyableReplayProvenance(t, messages, notice)
		})
	}
}

func replyableStoredNotice(t *testing.T, kind string) (workspaceStoreFixture, protocol.Message) {
	t.Helper()
	if kind == "outcome" {
		return importedOutcomeNotice(t)
	}
	f, result := reviewIDSource(t)
	return f, result.Msg
}

func replyableReplayProvenance(t *testing.T, messages []protocol.Message, original protocol.Message) {
	t.Helper()
	for _, message := range messages {
		if message.MessageID != original.MessageID {
			continue
		}
		pairEqual(t, message.Origin, protocol.OriginRemote)
		if message.Remote == nil || original.Remote == nil {
			t.Fatal("durable notice lost its remote reply workspace after restart")
		}
		pairEqual(t, message.Remote.Contact, original.Remote.Contact)
		pairEqual(t, message.Remote.Sender, original.Remote.Sender)
		pairEqual(t, message.From, original.From)
		pairEqual(t, message.InReplyTo, original.InReplyTo)
		return
	}
	t.Fatal("unacknowledged durable notice disappeared after restart")
}
