package store_test

import (
	"strings"
	"testing"
)

func TestStaleAskReplayAcceptancePreservesDeliveredHistoricalSnapshot(t *testing.T) {
	f, requested := importedOutcomeNotice(t)
	reason := "moved base=" + strings.Repeat("a", 40) + " head=" + strings.Repeat("b", 64)
	declined := staleStoreOutcome(t, f, reason)
	replay := staleStoreReplay(t, staleStoreRestart(t, f), 26)
	old := replay[requested.MessageID]
	current := replay[declined.MessageID]
	if old.ReviewOutcome == nil || current.ReviewOutcome == nil {
		t.Fatal("restart lost typed outcome snapshots")
	}
	pairEqual(t, old.ReviewOutcome.State, "requested")
	pairEqual(t, current.ReviewOutcome.State, "declined")
	pairEqual(t, current.ReviewOutcome.Reason, reason)
	pairEqual(t, staleStoreJSON(t, old.ReviewOutcome)["moved_to"], any(nil))
	moved := staleStoreJSON(t, current.ReviewOutcome)["moved_to"]
	if moved == nil {
		t.Fatal("restart lost reported moved pins")
	}
	pairEqual(t, staleStoreJSON(t, moved)["head"], any(strings.Repeat("b", 64)))
	pairEqual(t, current.InReplyTo, declined.InReplyTo)
	pairEqual(t, current.Remote.Contact, f.key)
}

func TestStaleAskReplayAcceptanceLegacyReplayTrimsNewMetadata(t *testing.T) {
	f, _ := importedOutcomeNotice(t)
	declined := staleStoreOutcome(t, f, "moved base="+strings.Repeat("a", 40)+" head="+strings.Repeat("b", 40))
	db := staleStoreRestart(t, f)
	legacy := staleStoreReplay(t, db, 25)[declined.MessageID]
	if legacy.ReviewOutcome == nil {
		t.Fatal("legacy replay lost compatible lifecycle metadata")
	}
	pairEqual(t, legacy.ReviewOutcome.State, "declined")
	pairEqual(t, staleStoreJSON(t, legacy.ReviewOutcome)["moved_to"], any(nil))
	modern := staleStoreReplay(t, db, 26)[declined.MessageID]
	if staleStoreJSON(t, modern.ReviewOutcome)["moved_to"] == nil {
		t.Fatal("legacy delivery permanently erased modern metadata")
	}
}

func TestStaleAskReplayAcceptanceMovedReasonParsingIsStrict(t *testing.T) {
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 64)
	for _, reason := range []string{"moved base=" + base + " head=" + head, "moved base=" + head + " head=" + base} {
		f, _ := importedOutcomeNotice(t)
		notice := staleStoreOutcome(t, f, reason)
		if staleStoreJSON(t, notice.ReviewOutcome)["moved_to"] == nil {
			t.Fatalf("valid 40/64 pin pair not parsed: %s", reason)
		}
	}
}

func TestStaleAskReplayAcceptanceMalformedReasonIsOnlyText(t *testing.T) {
	valid := "moved base=" + strings.Repeat("a", 40) + " head=" + strings.Repeat("b", 40)
	for _, reason := range []string{valid + " trailing", "prefix " + valid, strings.ToUpper(valid), "moved base=short head=short", "moved base=" + strings.Repeat("a", 40)} {
		t.Run(reason, func(t *testing.T) {
			f, _ := importedOutcomeNotice(t)
			notice := staleStoreOutcome(t, f, reason)
			pairEqual(t, notice.ReviewOutcome.Reason, reason)
			pairEqual(t, staleStoreJSON(t, notice.ReviewOutcome)["moved_to"], any(nil))
		})
	}
}
