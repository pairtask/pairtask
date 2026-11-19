package store_test

import (
	"context"
	"testing"
	"time"
)

func TestPairedClaimsRevokeAcceptanceUnshareSettlesOnlyRevokedNotices(t *testing.T) {
	for _, revoke := range []string{"worker", "*"} {
		t.Run(revoke, func(t *testing.T) {
			db, raw, contact := messageRawFixture(t)
			ctx := context.Background()
			s := db.PeerMessages()
			_, err := s.Share(ctx, contact, []string{"worker", "reviewer"}, messageAt)
			pairOK(t, err)
			for _, agent := range []string{"worker", "reviewer"} {
				_, err = raw.Exec(`INSERT INTO claim_notices_out(contact,pairing_epoch,remote_id,agent,role,task,state,seq,at)
SELECT identity_key,pairing_epoch,'subject',?,'owner','investigate','active',1,? FROM peer_contacts WHERE identity_key=?`, agent, messageAt.Format(time.RFC3339Nano), contact)
				pairOK(t, err)
			}
			pending, err := s.PendingClaimNotices(ctx, contact, 20)
			pairOK(t, err)
			pairEqual(t, len(pending), 2)
			_, err = s.Unshare(ctx, contact, []string{revoke}, messageAt.Add(time.Minute))
			pairOK(t, err)
			want := 1
			if revoke == "*" {
				want = 0
			}
			for _, regrant := range []bool{false, true} {
				if regrant {
					_, err = s.Share(ctx, contact, []string{"worker", "reviewer"}, messageAt.Add(2*time.Minute))
					pairOK(t, err)
				}
				pending, err = s.PendingClaimNotices(ctx, contact, 20)
				pairOK(t, err)
				pairEqual(t, len(pending), want)
				if want == 1 {
					pairEqual(t, pending[0].Agent, "reviewer")
				}
			}
			var pendingFlag int
			var reason string
			err = raw.QueryRow(`SELECT pending,dropped FROM claim_notices_out WHERE contact=? AND agent='worker'`, contact).Scan(&pendingFlag, &reason)
			pairOK(t, err)
			pairEqual(t, pendingFlag, 0)
			pairEqual(t, reason, "not_shared")
		})
	}
}
