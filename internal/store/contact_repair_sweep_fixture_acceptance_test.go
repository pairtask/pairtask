package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func sweepHistoryFixture(t *testing.T) workspaceStoreFixture {
	t.Helper()
	f := namespaceFixture(t)
	namespaceAccept(t, f, messageWire("history-shared", 1))
	namespaceRepair(t, f)
	next := messageWire("history-shared", 1)
	next.Text = "epoch2 history body"
	namespaceAccept(t, f, next)
	return f
}

func (f workspaceStoreFixture) sweepQueueAsk(t *testing.T) messaging.ReviewOutcome {
	t.Helper()
	m := remoteAsk("ask")
	_, err := f.db.PeerMessages().Queue(context.Background(), messaging.Outgoing{Contact: f.key, ID: m.ID, Text: m.Text, From: "sender", To: m.To, Review: m.Review, Now: messageAt})
	pairOK(t, err)
	return messaging.ReviewOutcome{AskID: m.ID, Reviewer: m.To[0], Seq: 1, State: "requested"}
}

func (f workspaceStoreFixture) sweepCloneAsks(t *testing.T, epochs int) {
	t.Helper()
	cols := epochColumns(t, f.raw, "review_asks_out")
	quoted, values := make([]string, len(cols)), make([]string, len(cols))
	for i, col := range cols {
		quoted[i], values[i] = epochQuote(col), epochQuote(col)
		if col == "pairing_epoch" {
			values[i] = "?"
		}
	}
	query := `INSERT INTO review_asks_out (` + strings.Join(quoted, ",") + `) SELECT ` + strings.Join(values, ",") + ` FROM review_asks_out WHERE contact=? AND pairing_epoch=1 AND ask_id='ask'`
	for epoch := 2; epoch <= epochs; epoch++ {
		_, err := f.raw.Exec(query, epoch, f.key)
		pairOK(t, err)
	}
	_, err := f.raw.Exec(`UPDATE peer_contacts SET pairing_epoch=?,generation=generation+? WHERE identity_key=?`, epochs, epochs, f.key)
	pairOK(t, err)
}

func sweepRememberEpochs(t *testing.T, asks []store.AskOut, seen map[int64]bool) {
	t.Helper()
	for _, ask := range asks {
		if ask.Epoch <= 0 {
			t.Fatalf("history ask lacks its source epoch: %+v", ask)
		}
		if seen[ask.Epoch] {
			t.Fatalf("history pagination repeated epoch %d", ask.Epoch)
		}
		seen[ask.Epoch] = true
	}
}
