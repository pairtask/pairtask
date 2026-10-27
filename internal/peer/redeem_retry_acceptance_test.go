//go:build unix

package peer_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

type redeemCommitFault struct {
	peer.PairingStore
	failures   int
	afterWrite bool
	seen       []peer.PairingOutcome
	advance    func()
}

func (s *redeemCommitFault) Commit(ctx context.Context, o peer.PairingOutcome) (peer.PairingRequest, error) {
	o.TranscriptID = bytes.Clone(o.TranscriptID)
	s.seen = append(s.seen, o)
	s.advance()
	if len(s.seen) > s.failures {
		return s.PairingStore.Commit(ctx, o)
	}
	if s.afterWrite {
		if _, err := s.PairingStore.Commit(ctx, o); err != nil {
			return peer.PairingRequest{}, err
		}
	}
	return peer.PairingRequest{}, errors.New("injected commit failure")
}
func TestRedeemAcceptanceCommitRetriesKeepOneFrozenOutcome(t *testing.T) {
	cases := []struct {
		name       string
		failures   int
		afterWrite bool
	}{{"transient", 2, false}, {"ambiguous", 1, true}, {"persistent", 99, false}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRedeemFixture(t)
			fault := &redeemCommitFault{PairingStore: peer.NewMemPairingStore(), failures: c.failures, afterWrite: c.afterWrite}
			r.inv = newInvitationFixture(t, fault)
			r.open = openInvitation(t, r.inv)
			fault.advance = func() { r.inv.clock.advance(time.Second) }
			result := r.run(t, r.code())
			redeemCheckCommitResult(t, r, fault, result)
		})
	}
}
func redeemCheckCommitResult(t *testing.T, r *redeemFixture, fault *redeemCommitFault, result redeemResult) {
	t.Helper()
	invEqual(t, r.inviteEx.invites, 1)
	invEqual(t, r.joinEx.confirmations, 1)
	invEqual(t, len(fault.seen), min(3, fault.failures+1))
	for _, o := range fault.seen[1:] {
		invEqual(t, reflect.DeepEqual(o, fault.seen[0]), true)
	}
	invEqual(t, invitationStatus(t, r.inv).AttemptsLeft, 2)
	if fault.failures >= 3 {
		invEqual(t, errors.Is(result.joinErr, peer.ErrJoinOutcomeUnknown), true)
		invEqual(t, invitationStatus(t, r.inv).InFlight, true)
		redeemNoOutcome(t, r)
		r.inv.clock.advance(31 * time.Second)
		invEqual(t, invitationStatus(t, r.inv).InFlight, false)
		return
	}
	invOK(t, result.joinErr)
	invOK(t, result.inviteErr)
	invEqual(t, invitationStatus(t, r.inv).Consumed, true)
	rows, err := fault.Requests(context.Background())
	invOK(t, err)
	invEqual(t, len(rows), 1)
}
