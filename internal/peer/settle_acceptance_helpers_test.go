//go:build unix

package peer_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
)

type settleScript struct {
	in   [][]byte
	sent [][]byte
}

func (s *settleScript) Send(frame []byte) error {
	s.sent = append(s.sent, bytes.Clone(frame))
	return nil
}
func (s *settleScript) Recv() ([]byte, error) {
	if len(s.in) == 0 {
		return nil, io.EOF
	}
	frame := s.in[0]
	s.in = s.in[1:]
	return frame, nil
}

func settleJoiner(t *testing.T, r *redeemFixture) *peer.Joiner {
	t.Helper()
	j, err := peer.NewJoiner(r.joinEx, r.joins, r.bob, r.inv.clock.read, rand.Reader)
	invOK(t, err)
	return j
}

func settleInviter(t *testing.T, r *redeemFixture) *peer.Inviter {
	t.Helper()
	i, err := peer.NewInviter(r.inviteEx, r.inv.manager, r.alice)
	invOK(t, err)
	return i
}

func settleStored(t *testing.T, r *redeemFixture, op string) peer.JoinRecord {
	t.Helper()
	row, found, err := r.joins.GetJoin(context.Background(), op)
	invOK(t, err)
	invEqual(t, found, true)
	return row
}

func settleRequest(t *testing.T, r *redeemFixture) peer.PairingRequest {
	t.Helper()
	row, found, err := r.inv.store.Request(context.Background(), r.bob.Fingerprint())
	invOK(t, err)
	invEqual(t, found, true)
	return row
}

func settleFrame(t *testing.T, row peer.JoinRecord) []byte {
	t.Helper()
	b, err := peer.EncodeSettle(peer.Settle{JoinerOperationID: row.OperationID, InviterOperationID: row.InviterOperationID,
		InvitationID: row.InvitationID, TranscriptID: row.TranscriptID})
	invOK(t, err)
	return b
}

func settlePair(t *testing.T, r *redeemFixture, op string, joinFilter, inviteFilter func([]byte) ([]byte, error)) redeemResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, b := make(chan []byte, 4), make(chan []byte, 4)
	var jf peer.Frames = redeemPipe{ctx, a, b}
	var inf peer.Frames = redeemPipe{ctx, b, a}
	if joinFilter != nil {
		jf = redeemFilter{jf, joinFilter}
	}
	if inviteFilter != nil {
		inf = redeemFilter{inf, inviteFilter}
	}
	j, i := settleJoiner(t, r), settleInviter(t, r)
	results := make(chan redeemSide, 2)
	go func() {
		defer close(b)
		row, err := j.Settle(ctx, jf, peer.Connection{PeerLeaf: r.alice.Fingerprint()}, op)
		results <- redeemSide{join: true, row: row, err: err}
	}()
	go func() {
		defer close(a)
		err := i.ServeSettle(ctx, inf, peer.Connection{PeerLeaf: r.bob.Fingerprint()})
		results <- redeemSide{err: err}
	}()
	var result redeemResult
	for range 2 {
		s := <-results
		if s.join {
			result.row, result.joinErr = s.row, s.err
		} else {
			result.inviteErr = s.err
		}
	}
	return result
}

func settleReady(t *testing.T, decision peer.PairingState) (*redeemFixture, peer.JoinRecord) {
	t.Helper()
	r := newRedeemFixture(t)
	paired := r.run(t, r.code())
	invOK(t, paired.joinErr)
	invOK(t, paired.inviteErr)
	var err error
	switch decision {
	case peer.PairingAccepted:
		_, err = r.inv.store.Accept(context.Background(), r.bob.Fingerprint())
	case peer.PairingRejected:
		_, err = r.inv.store.Reject(context.Background(), r.bob.Fingerprint())
	}
	invOK(t, err)
	return r, paired.row
}

func settleContactCount(t *testing.T, source any, want int) {
	t.Helper()
	rows, err := source.(peer.ContactStore).Contacts(context.Background())
	invOK(t, err)
	invEqual(t, len(rows), want)
}

var errSettleDisk = errors.New("injected settlement write failure")

type settleFaultJoins struct {
	peer.JoinStore
	failDecision, failConfirmation bool
}

func (s *settleFaultJoins) RecordDecision(ctx context.Context, op string, decision peer.PairingState, at time.Time) (peer.JoinRecord, error) {
	if s.failDecision {
		s.failDecision = false
		return peer.JoinRecord{}, errSettleDisk
	}
	return s.JoinStore.RecordDecision(ctx, op, decision, at)
}

func (s *settleFaultJoins) RecordDecisionConfirmed(ctx context.Context, op string, decision peer.PairingState, at time.Time) (peer.JoinRecord, error) {
	if s.failConfirmation {
		s.failConfirmation = false
		return peer.JoinRecord{}, errSettleDisk
	}
	return s.JoinStore.RecordDecisionConfirmed(ctx, op, decision, at)
}

type settleFaultPairs struct {
	peer.PairingStore
	fail bool
}

func (s *settleFaultPairs) Deliver(ctx context.Context, key string, decision peer.PairingState, at time.Time) (peer.PairingRequest, error) {
	if s.fail {
		s.fail = false
		return peer.PairingRequest{}, errSettleDisk
	}
	return s.PairingStore.Deliver(ctx, key, decision, at)
}
