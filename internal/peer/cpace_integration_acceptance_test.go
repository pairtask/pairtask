//go:build unix

package peer_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/pake"
	"github.com/pairtask/pairtask/internal/store"
)

func TestCPaceIntegrationAcceptancePersistsMatchingOutcomes(t *testing.T) {
	r := newRedeemFixture(t)
	r.filter = r.checkDurableReceipt
	result := r.runWith(t, r.code(), pake.New(), pake.New())
	invOK(t, result.joinErr)
	invOK(t, result.inviteErr)
	rows := redeemRows(t, r.joins)
	invEqual(t, len(rows), 1)
	row := rows[0]
	invEqual(t, row.State, peer.JoinAwaitingAcceptance)
	invEqual(t, row.Peer, r.alice.Fingerprint())
	outcome, found, err := r.inv.store.Outcome(context.Background(), row.InviterOperationID)
	invOK(t, err)
	invEqual(t, found, true)
	invEqual(t, outcome.IdentityKey, r.bob.Fingerprint())
	invEqual(t, bytes.Equal(outcome.TranscriptID, row.TranscriptID), true)
	invEqual(t, outcome.InvitationID, row.InvitationID)
	invEqual(t, invitationStatus(t, r.inv).Consumed, true)
}
func TestCPaceIntegrationAcceptanceWrongCodeUsesThreeAttempts(t *testing.T) {
	r := newRedeemFixture(t)
	wrong := r.code()
	wrong[0] = '0' + (wrong[0]-'0'+1)%10
	for n := range 3 {
		result := r.runWith(t, wrong, pake.New(), pake.New())
		invError(t, result.joinErr)
		invError(t, result.inviteErr)
		invEqual(t, invitationStatus(t, r.inv).AttemptsLeft, 2-n)
		redeemNoOutcome(t, r)
	}
	result := r.runWith(t, r.code(), pake.New(), pake.New())
	invEqual(t, errors.Is(result.inviteErr, peer.ErrInvitationExhausted), true)
	invEqual(t, errors.Is(result.joinErr, peer.ErrJoinRefused), true)
	invEqual(t, invitationStatus(t, r.inv).Consumed, false)
}
func TestCPaceIntegrationAcceptancePersistFailureCannotEnroll(t *testing.T) {
	r := newRedeemFixture(t)
	r.joins = redeemFaultStore{JoinStore: r.joins, verifyErr: errors.New("disk unavailable")}
	result := r.runWith(t, r.code(), pake.New(), pake.New())
	invError(t, result.joinErr)
	invError(t, result.inviteErr)
	redeemNoOutcome(t, r)
	invEqual(t, invitationStatus(t, r.inv).Consumed, false)
	invEqual(t, invitationStatus(t, r.inv).AttemptsLeft, 2)
	invEqual(t, redeemRows(t, r.joins)[0].State, peer.JoinFailed)
}
func cpaceSQLite(t *testing.T, path string) *store.Store {
	t.Helper()
	db, err := store.Open(path)
	invOK(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}
func TestCPaceIntegrationAcceptanceLostReceiptSurvivesBothRestarts(t *testing.T) {
	ctx := context.Background()
	alicePath := filepath.Join(t.TempDir(), "alice.db")
	bobPath := filepath.Join(t.TempDir(), "bob.db")
	aliceDB, bobDB := cpaceSQLite(t, alicePath), cpaceSQLite(t, bobPath)
	r := newRedeemFixture(t)
	r.alice = identityLoad(t, aliceDB.PeerDir(), aliceDB, rand.Reader)
	r.bob = identityLoad(t, bobDB.PeerDir(), bobDB, rand.Reader)
	r.inv = newInvitationFixture(t, aliceDB)
	r.open = openInvitation(t, r.inv)
	r.joins = bobDB
	r.filter = func(frame []byte) ([]byte, error) {
		if len(frame) < 2 || frame[1] != peer.FrameReceipt {
			return frame, nil
		}
		return nil, io.ErrClosedPipe
	}
	result := r.runWith(t, r.code(), pake.New(), pake.New())
	invEqual(t, errors.Is(result.joinErr, peer.ErrJoinOutcomeUnknown), true)
	row := redeemRows(t, bobDB)[0]
	invEqual(t, row.State, peer.JoinOutcomeUnknown)
	invOK(t, aliceDB.Close())
	invOK(t, bobDB.Close())
	aliceDB, bobDB = cpaceSQLite(t, alicePath), cpaceSQLite(t, bobPath)
	alice := identityLoad(t, aliceDB.PeerDir(), aliceDB, bytes.NewReader(nil))
	bob := identityLoad(t, bobDB.PeerDir(), bobDB, bytes.NewReader(nil))
	invEqual(t, alice.Fingerprint(), row.Peer)
	invEqual(t, bob.Fingerprint(), row.Joiner)
	_, err := bobDB.RecoverJoins(ctx, r.inv.clock.read())
	invOK(t, err)
	restored, found, err := bobDB.GetJoin(ctx, row.OperationID)
	invOK(t, err)
	invEqual(t, found, true)
	invEqual(t, restored.State, peer.JoinOutcomeUnknown)
	invEqual(t, bytes.Equal(restored.TranscriptID, row.TranscriptID), true)
	receipt := peer.JoinReceipt{InvitationID: row.InvitationID, InviterOperationID: row.InviterOperationID, Joiner: row.Joiner, TranscriptID: row.TranscriptID}
	ok, err := peer.MatchReceipt(ctx, aliceDB, receipt)
	invOK(t, err)
	invEqual(t, ok, true)
	// Exercise the durable transition only; authenticated reconciliation
	// transport is deliberately not claimed by this provider-only test.
	settled, err := bobDB.RecordReceipt(ctx, row.OperationID, receipt, r.inv.clock.read())
	invOK(t, err)
	invEqual(t, settled.State, peer.JoinAwaitingAcceptance)
	requests, err := aliceDB.Requests(ctx)
	invOK(t, err)
	invEqual(t, len(requests), 1)
	invEqual(t, requests[0].State, peer.PairingPending)
}
