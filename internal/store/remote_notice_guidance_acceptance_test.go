package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func noticeGuidanceImport(t *testing.T) (*store.Store, string, store.ImportResult) {
	t.Helper()
	db, key := messageFixture(t)
	routingRegister(t, db, "reviewer")
	reviewGrant(t, db, key, store.ReviewAuto)
	m := remoteAsk("guidance-ask")
	a, err := db.PeerMessages().Accept(context.Background(), key, m, messageAt)
	pairOK(t, err)
	pairEqual(t, a.Stored, true)
	got, err := db.ImportRemote(context.Background(), store.RemoteImport{Contact: key, RemoteID: m.ID}, 1, messageAt)
	pairOK(t, err)
	pairEqual(t, got.State, store.ImportImported)
	return db, key, got
}

func TestRemoteNoticeGuidanceAcceptanceNamesLifecycleAndNoticeIdentities(t *testing.T) {
	_, _, got := noticeGuidanceImport(t)
	var body struct {
		Text string `json:"text"`
	}
	pairOK(t, json.Unmarshal(got.Msg.Payload, &body))
	for _, part := range []string{"review_id " + got.ReviewID, got.Msg.MessageID, "acknowledge", "message_id", "accept_review", "decline_review", "submit_review", "automatically", "no chat reply is needed"} {
		if !strings.Contains(body.Text, part) {
			t.Fatalf("synthetic notice lacks guidance %q: %s", part, body.Text)
		}
	}
}

func TestRemoteNoticeGuidanceAcceptanceKeepsProvenancePinsAndDelivery(t *testing.T) {
	db, key, got := noticeGuidanceImport(t)
	msg := got.Msg
	pairEqual(t, msg.MessageID, "review:"+got.ReviewID)
	pairEqual(t, msg.ReviewID, got.ReviewID)
	pairEqual(t, msg.Origin, protocol.OriginRemote)
	pairEqual(t, msg.From, "sender/@\nverbatim")
	pairEqual(t, msg.To, "reviewer")
	pairEqual(t, msg.Durable, true)
	pairEqual(t, msg.AckRequested, true)
	if msg.Remote == nil {
		t.Fatal("notice lost remote provenance")
	}
	pairEqual(t, msg.Remote.Contact, key)
	pairEqual(t, msg.Remote.Sender, msg.From)
	rec, found, err := db.GetReview(context.Background(), got.ReviewID)
	pairOK(t, err)
	pairEqual(t, found, true)
	if rec.View.Pinned == nil {
		t.Fatal("notice review lost immutable pins")
	}
	pairEqual(t, rec.View.Pinned.Base, "base-sha")
	pairEqual(t, rec.View.Pinned.Head, "head-sha")
	pairEqual(t, rec.View.Spec.Policy.Publish, false)
	pairEqual(t, rec.View.Spec.Policy.Formal, false)
	replayed, err := db.ImportRemote(context.Background(), store.RemoteImport{Contact: key, RemoteID: "guidance-ask"}, 2, messageAt)
	pairOK(t, err)
	pairEqual(t, replayed.Changed, false)
}
