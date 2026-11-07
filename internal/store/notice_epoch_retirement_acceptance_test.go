package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestNoticeEpochRetirementAcceptanceInstallSupersedesPriorPairingNotice(t *testing.T) {
	f := installFixture(t)
	f.installedEpoch(t, 101)
	out, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "retire-old-notice"))
	pairOK(t, err)
	f.installedEpoch(t, 102)
	pairEqual(t, noticeDeliveryState(t, f, out.Generation), "superseded")
	due, err := f.db.PeerMessages().PendingNotices(context.Background(), messageAt.Add(time.Hour))
	pairOK(t, err)
	pairEqual(t, len(due), 0)
}
func TestNoticeEpochRetirementAcceptanceSchedulerFiltersOldIDDespitePendingState(t *testing.T) {
	f := installFixture(t)
	f.installedEpoch(t, 101)
	out, _, err := f.db.UnpairContact(context.Background(), unpairRequest(t, f, "filter-old-notice"))
	pairOK(t, err)
	f.installedEpoch(t, 102)
	// Simulate a retained legacy branch row whose delivery state is still pending.
	_, err = f.raw.Exec(`UPDATE contact_notices_out SET state='pending' WHERE contact=? AND gen=?`, f.contact, out.Generation)
	pairOK(t, err)
	due, err := f.db.PeerMessages().PendingNotices(context.Background(), messageAt.Add(time.Hour))
	pairOK(t, err)
	pairEqual(t, len(due), 0)
}
func TestContactCapsRetainedRequestsAcceptancePast1024StillAdmitsNewEpoch(t *testing.T) {
	f := installFixture(t)
	f.installedEpoch(t, 101)
	seedContactCaps(t, f, 0, 1024)
	// Real retired pairings retain their pairing request and outcome history,
	// so the capacity fixture must include those rows as well as contacts.
	pairSeedSQLite(t, f.raw, 1024, func(n int) peer.PairingOutcome { return boundedOutcome(9999 + n) }, peer.PairingAccepted)
	candidate := *f
	candidate.contact = joinKey(9005)
	pairOK(t, capTryInstall(t, &candidate, 102))
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts`), 1026)
	pairEqual(t, epochCount(t, f.raw, `SELECT count(*) FROM peer_contacts WHERE unpaired_at=''`), 2)
}
