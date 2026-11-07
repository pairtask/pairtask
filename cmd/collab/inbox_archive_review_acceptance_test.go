package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestInboxArchiveAcceptanceUnfinishedReplyAboveUpperKeepsParent(t *testing.T) {
	for _, reason := range []string{"unacknowledged", "claim"} {
		t.Run(reason, func(t *testing.T) {
			f := newOfflineInbox(t)
			f.seed(t, offlineSeed{id: "finished"})
			parent := f.seed(t, offlineSeed{id: "parent"})
			offlineUpperChild(t, f, reason)
			original := f.payload(t, "parent")
			code, stdout, stderr := f.call(t, "archive", "--confirm", "--upper", fmt.Sprint(parent.Seq))
			assertStatusEqual(t, stderr, code, 0)
			result := offlineResult(t, stdout)
			f.archived(t, "finished")
			assertStatusEqual(t, "unfinished later reply keeps parent body", f.payload(t, "parent"), original)
			kept := offlineMap(t, result["kept_by_reason"])
			assertStatusEqual(t, "child counted once above upper", offlineJSONCount(t, kept, "after_upper"), int64(1))
			assertStatusEqual(t, "parent counted as reply context", offlineJSONCount(t, kept, "reply_child"), int64(1))
		})
	}
}

func offlineUpperChild(t *testing.T, f *offlineInboxFixture, reason string) {
	t.Helper()
	if reason == "unacknowledged" {
		f.seed(t, offlineSeed{id: "later-reply", parent: "parent", pending: "operator"})
		return
	}
	f.seed(t, offlineSeed{id: "later-reply", parent: "parent", operator: true, targets: []string{"worker"}})
	f.protect(t, "claim", "later-reply")
}

func TestInboxArchiveAcceptanceRequiresCommittedSchemaMarker(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "original"})
	f.exec(t, `DELETE FROM schema_steps WHERE name='pairing_epoch'`)
	before := offlineSnapshot(t, f.database)
	code, _, stderr := f.call(t, "archive", "--confirm")
	if code == 0 {
		t.Fatal("archive accepted a database missing its committed schema marker")
	}
	if !strings.Contains(strings.ToLower(stderr), "broker") {
		t.Fatalf("missing start-current-broker guidance: %s", stderr)
	}
	assertStatusEqual(t, "schema marker refusal leaves source unchanged", offlineSnapshot(t, f.database), before)
}
