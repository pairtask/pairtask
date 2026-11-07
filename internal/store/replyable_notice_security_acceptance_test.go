package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pairtask/pairtask/internal/peer/messaging"
	"github.com/pairtask/pairtask/internal/store"
)

func TestReplyableNoticeAcceptanceReviewCorrelationRequiresOriginalParties(t *testing.T) {
	for _, change := range []string{"valid", "wrong-author", "wrong-recipient"} {
		t.Run(change, func(t *testing.T) {
			f, review := reviewIDSource(t)
			routingRegister(t, f.db, "other-reviewer")
			_, err := f.db.PeerMessages().Share(context.Background(), f.key, []string{"reviewer", "other-reviewer"}, messageAt)
			pairOK(t, err)
			message := replyableIncoming(change)
			accepted, err := f.db.PeerMessages().Accept(context.Background(), f.key, message, messageAt)
			pairOK(t, err)
			pairEqual(t, accepted.Stored, true)
			got, err := f.db.ImportRemote(context.Background(), store.RemoteImport{Contact: f.key, RemoteID: message.ID}, 2, messageAt)
			pairOK(t, err)
			pairEqual(t, got.State, store.ImportImported)
			want := ""
			if change == "valid" {
				want = review.ReviewID
			}
			pairEqual(t, got.Msg.InReplyTo, want)
		})
	}
}

func replyableIncoming(change string) messaging.Message {
	message := messaging.Message{ID: "outcome-follow-up", N: 2, From: "sender/@\nverbatim", To: []string{"reviewer"},
		ReplyTo: messaging.ReplyRef{ID: "source-ask", Author: messaging.ReplyBySender}, Text: "ordinary follow-up", CreatedAt: messageAt}
	if change == "wrong-author" {
		message.From = "another-requester"
	}
	if change == "wrong-recipient" {
		message.To = []string{"other-reviewer"}
	}
	return message
}

func TestReplyableNoticeAcceptanceRefusalKindsRemainUnreplyable(t *testing.T) {
	for _, remoteID := range []string{"refused·ask", "refusals·sender·1"} {
		t.Run(remoteID, func(t *testing.T) {
			f, notice := importedOutcomeNotice(t)
			// Both refusal kinds share the lifecycle outcome import kind.
			// Keep the imported row/audience valid while varying its subtype.
			_, err := f.raw.Exec(`UPDATE remote_imports SET remote_id=? WHERE local_id=?`, remoteID, notice.MessageID)
			pairOK(t, err)
			var before int
			pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM peer_messages WHERE direction='out'`).Scan(&before))
			_, err = f.db.QueueRemoteReply(context.Background(), outcomeStoreReply(notice))
			pairEqual(t, errors.Is(err, store.ErrOutcomeNotice), true)
			var after int
			pairOK(t, f.raw.QueryRow(`SELECT COUNT(*) FROM peer_messages WHERE direction='out'`).Scan(&after))
			pairEqual(t, after, before)
		})
	}
}
