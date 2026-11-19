package desktop

import "testing"

func TestInboxSyncNoticeAcceptanceReportedOncePerConsumer(t *testing.T) {
	f := newIncrementalFixture(t)
	f.incoming(t, 1)
	first := incrementalRead(t, f.node, map[string]any{"consumer": "chat"})
	if len(first) != 1 || first[0].SyncFailure == "" {
		t.Fatalf("offline fixture must report its initial sync failure: %+v", first)
	}
	again := incrementalRead(t, f.node, map[string]any{"consumer": "chat"})
	incrementalIDs(t, again, "in-1")
	if again[0].SyncFailure != "" {
		t.Fatalf("unchanged sync failure repeated: %q", again[0].SyncFailure)
	}
	other := incrementalRead(t, f.node, map[string]any{"consumer": "cowork"})
	if len(other) != 1 || other[0].SyncFailure == "" {
		t.Fatal("one consumer suppressed another consumer's first sync notice")
	}
}
