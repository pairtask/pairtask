package main

import (
	"testing"
)

func TestInboxArchiveAcceptanceProtectsEveryRecipientAndUnfinishedReference(t *testing.T) {
	for _, reason := range []string{"claim", "assignment", "pending_audit", "paired", "peer_linked", "pending_import", "unsettled_ask", "legacy_unacknowledged"} {
		t.Run(reason, func(t *testing.T) {
			f := newOfflineInbox(t)
			f.seed(t, offlineSeed{id: "done"})
			f.seed(t, offlineSeed{id: "protected", operator: true, targets: []string{"worker"}})
			f.protect(t, reason, "protected")
			original := f.payload(t, "protected")
			result := f.archive(t)
			f.archived(t, "done")
			assertStatusEqual(t, "protected payload", f.payload(t, "protected"), original)
			assertStatusEqual(t, "kept reason", offlineJSONCount(t, offlineMap(t, result["kept_by_reason"]), reason), int64(1))
		})
	}
}

func TestInboxArchiveAcceptanceOneUnacknowledgedRecipientProtectsWholeMessage(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "done"})
	f.seed(t, offlineSeed{id: "mixed", targets: []string{"operator", "reader"}, pending: "reader"})
	original := f.payload(t, "mixed")
	result := f.archive(t)
	f.archived(t, "done")
	assertStatusEqual(t, "one pending recipient keeps complete body", f.payload(t, "mixed"), original)
	assertStatusEqual(t, "one message protected", offlineJSONCount(t, offlineMap(t, result["kept_by_reason"]), "unacknowledged"), int64(1))
}

func TestInboxArchiveAcceptanceProtectsTransitiveReplyContextAndTerminatesCycles(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		f := newOfflineInbox(t)
		f.seed(t, offlineSeed{id: "done"})
		f.seed(t, offlineSeed{id: "grandparent"})
		f.seed(t, offlineSeed{id: "parent", parent: "grandparent"})
		f.seed(t, offlineSeed{id: "unfinished", parent: "parent", pending: "operator"})
		if cycle {
			f.exec(t, `UPDATE message_metadata SET in_reply_to='parent' WHERE message_id='grandparent'`)
		}
		f.archive(t)
		f.archived(t, "done")
		assertStatusEqual(t, "grandparent context", f.payload(t, "grandparent"), `{"text":"original-body-grandparent"}`)
		assertStatusEqual(t, "parent context", f.payload(t, "parent"), `{"text":"original-body-parent"}`)
	}
}

func TestInboxArchiveAcceptanceKeepsOpenReviewNotice(t *testing.T) {
	f := newOfflineInbox(t)
	f.seed(t, offlineSeed{id: "done"})
	f.seed(t, offlineSeed{id: "review:review-one"})
	f.exec(t, `INSERT INTO reviews (review_id,reviewer,state,hash,doc,created_at,updated_at) VALUES ('review-one','worker','in_review','hash','{}','2026-11-07T00:00:00Z','2026-11-07T00:00:00Z')`)
	result := f.archive(t)
	f.archived(t, "done")
	assertStatusEqual(t, "open review body", f.payload(t, "review:review-one"), `{"text":"original-body-review:review-one"}`)
	assertStatusEqual(t, "review reason", offlineJSONCount(t, offlineMap(t, result["kept_by_reason"]), "review"), int64(1))
}

func TestInboxArchiveAcceptanceCompletedClaimsAndAssignmentsDoNotPinHistoryForever(t *testing.T) {
	for _, reason := range []string{"claim", "assignment"} {
		f := newOfflineInbox(t)
		f.seed(t, offlineSeed{id: "done"})
		f.seed(t, offlineSeed{id: "finished-work", operator: true, targets: []string{"worker"}})
		f.protect(t, reason, "finished-work")
		query := `UPDATE claims SET state='done',finished_at='2026-11-07T01:00:00Z'`
		if reason == "assignment" {
			query = `UPDATE assignments SET state='done',finished_at='2026-11-07T01:00:00Z'`
		}
		f.exec(t, query)
		f.archive(t)
		f.archived(t, "done")
		f.archived(t, "finished-work")
	}
}
