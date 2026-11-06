package store_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestContactRepairSweepAcceptanceOutcomeFindingsChangeOnlyCurrentAsk(t *testing.T) {
	f := namespaceFixture(t)
	f.sweepQueueAsk(t)
	namespaceRepair(t, f)
	old := namespaceEpochRows(t, f, "review_asks_out", 1)
	namespaceShare(t, f, "sender")
	outcome := f.sweepQueueAsk(t)
	outcome.State, outcome.Attempt, outcome.Verdict = "completed", 1, "comment"
	outcome = snapshotOutcome(t, outcome, snapshotFindings)
	answer, err := wireBound(t, f).AcceptOutcome(context.Background(), f.key, outcome, messageAt)
	pairOK(t, err)
	pairEqual(t, answer.Stored, true)
	current, found, err := f.db.PeerMessages().AskOutFor(context.Background(), f.key, "ask")
	pairOK(t, err)
	pairEqual(t, found, true)
	pairEqual(t, current.Epoch, int64(2))
	pairEqual(t, current.State, "completed")
	if current.Findings == nil {
		t.Fatal("current ask lost its findings snapshot")
	}
	if !reflect.DeepEqual(old, namespaceEpochRows(t, f, "review_asks_out", 1)) {
		t.Fatal("current outcome or findings rewrote retired ask")
	}
}

func TestContactRepairSweepAcceptanceRetiredOnlyAskIsUnknownToNewSession(t *testing.T) {
	f := namespaceFixture(t)
	outcome := f.sweepQueueAsk(t)
	namespaceRepair(t, f)
	before := wireMutationSnapshot(t, f)
	answer, err := wireBound(t, f).AcceptOutcome(context.Background(), f.key, outcome, messageAt)
	pairOK(t, err)
	pairEqual(t, answer.Stored, false)
	if !reflect.DeepEqual(before, wireMutationSnapshot(t, f)) {
		t.Fatal("retired-only ask outcome changed state")
	}
	_, found, err := f.db.PeerMessages().AskOutFor(context.Background(), f.key, "ask")
	pairOK(t, err)
	pairEqual(t, found, false)
}

func TestContactRepairSweepAcceptanceAsksByPagesTiedEpochHistoryWithoutSkipping(t *testing.T) {
	f := namespaceFixture(t)
	f.sweepQueueAsk(t)
	namespaceRepair(t, f)
	f.sweepCloneAsks(t, store.AsksLimit+1)
	seen := map[int64]bool{}
	before := ""
	for pass := 0; pass < 3; pass++ {
		page, err := f.db.AsksBy(context.Background(), "sender", before)
		pairOK(t, err)
		if len(page.Asks) == 0 {
			t.Fatal("history cursor skipped remaining tied epochs")
		}
		sweepRememberEpochs(t, page.Asks, seen)
		before = page.Cursors[len(page.Cursors)-1]
		if !page.More {
			break
		}
	}
	pairEqual(t, len(seen), store.AsksLimit+1)
}

func TestContactRepairSweepAcceptanceRefusalCannotSettleRetiredReusedAsk(t *testing.T) {
	f := namespaceFixture(t)
	f.sweepQueueAsk(t)
	namespaceRepair(t, f)
	old := namespaceEpochRows(t, f, "review_asks_out", 1)
	namespaceShare(t, f, "sender")
	f.sweepQueueAsk(t)
	pairOK(t, wireBound(t, f).Refused(context.Background(), f.key, messaging.Refused{ID: "ask", Refusal: messaging.Refuse(messaging.ReasonNotShared)}))
	var state string
	pairOK(t, f.raw.QueryRow(`SELECT state FROM review_asks_out WHERE pairing_epoch=2 AND ask_id='ask'`).Scan(&state))
	pairEqual(t, state, "refused")
	if !reflect.DeepEqual(old, namespaceEpochRows(t, f, "review_asks_out", 1)) {
		t.Fatal("refusal settlement rewrote retired ask")
	}
}
