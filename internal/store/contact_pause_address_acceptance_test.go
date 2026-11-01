package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/store"
)

func TestContactPauseAddressAcceptanceOldProbeCannotCommitAcrossPauseOrResume(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "paused", true: "resumed"}[resume], func(t *testing.T) {
			f := newOperatorContactFixture(t)
			move := addressMove(t, f, "old-probe", "old-probe.example:7445")
			before := contactGet(t, f.db, f.contact)
			pauseApply(t, f, pauseRequest(t, f, "pause", true))
			if resume {
				pauseApply(t, f, pauseRequest(t, f, "resume", false))
			}
			_, err := f.db.ChangeContactAddress(context.Background(), move)
			pairEqual(t, errors.Is(err, store.ErrFenced), true)
			pairEqual(t, contactGet(t, f.db, f.contact).Destination, before.Destination)
			pairEqual(t, contactGet(t, f.db, f.contact).DestinationAt, before.DestinationAt)
		})
	}
}

func TestContactPauseAddressAcceptanceNewProbeRefusedUntilResume(t *testing.T) {
	f := newOperatorContactFixture(t)
	ctx := context.Background()
	pauseApply(t, f, pauseRequest(t, f, "pause", true))
	_, _, err := f.db.BeginContactAddress(ctx, store.AddressOp{ID: "blocked-probe", Contact: f.contact, Candidate: "new.example:7445", At: time.Now()})
	pairEqual(t, errors.Is(err, store.ErrPaused), true)
	var count int
	pairOK(t, f.raw.QueryRow("SELECT COUNT(*) FROM contact_address_ops WHERE message_id='blocked-probe'").Scan(&count))
	pairEqual(t, count, 0)
	pauseApply(t, f, pauseRequest(t, f, "resume", false))
	move := addressMove(t, f, "fresh-probe", "new.example:7445")
	c, err := f.db.ChangeContactAddress(ctx, move)
	pairOK(t, err)
	pairEqual(t, c.Destination, move.To)
}
