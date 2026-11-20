package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func requestedPagePins(t *testing.T, value any, base, head string) {
	t.Helper()
	raw, err := json.Marshal(value)
	pairOK(t, err)
	var row struct {
		Forge     string                      `json:"forge"`
		Requested struct{ Base, Head string } `json:"requested"`
	}
	pairOK(t, json.Unmarshal(raw, &row))
	pairEqual(t, row.Forge, "github")
	pairEqual(t, row.Requested.Base, base)
	pairEqual(t, row.Requested.Head, head)
}

func TestRequestedPinsPageAcceptanceStoredAskSurvivesListAndDetailReads(t *testing.T) {
	db, key := messageFixture(t)
	ctx := context.Background()
	msg := remoteAsk("original-pins")
	msg.Review.Base, msg.Review.Head = strings.Repeat("1", 40), strings.Repeat("2", 40)
	_, err := db.PeerMessages().Queue(ctx, messaging.Outgoing{Contact: key, ID: msg.ID, Text: msg.Text, To: msg.To, Review: msg.Review, Now: messageAt})
	pairOK(t, err)
	ref := protocol.ReviewAskRef{Contact: key, AskID: msg.ID}
	before, found, err := db.PeerMessages().AskOutIn(ctx, store.MessageKey{Contact: key, ID: msg.ID})
	pairOK(t, err)
	pairEqual(t, found, true)
	list, err := db.AskedReviews(ctx, "")
	pairOK(t, err)
	pairEqual(t, len(list.Items), 1)
	requestedPagePins(t, list.Items[0], msg.Review.Base, msg.Review.Head)
	detail, err := db.AskedDetail(ctx, ref)
	pairOK(t, err)
	pairEqual(t, len(detail.Items), 1)
	requestedPagePins(t, detail.Items[0], msg.Review.Base, msg.Review.Head)
	after, found, err := db.PeerMessages().AskOutIn(ctx, store.MessageKey{Contact: key, ID: msg.ID})
	pairOK(t, err)
	pairEqual(t, found, true)
	old, _ := json.Marshal(before)
	now, _ := json.Marshal(after)
	pairEqual(t, string(now), string(old))
}
