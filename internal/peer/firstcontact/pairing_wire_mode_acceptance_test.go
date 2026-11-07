//go:build unix

package firstcontact_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/firstcontact"
	"github.com/pairtask/pairtask/internal/peer/pake"
	"github.com/pairtask/pairtask/internal/store"
)

type pairingWireStack struct {
	inviter    *peer.Inviter
	joiner     *peer.Joiner
	alice, bob *store.Store
	joined     peer.JoinRecord
}

func pairingWireStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/pairing.db")
	mustOK(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func pairingWireJoin(t *testing.T, c pairingTLSConnections) pairingWireStack {
	t.Helper()
	alice, bob := pairingWireStore(t), pairingWireStore(t)
	invitations, err := peer.NewInvitations(peer.DefaultInvitationPolicy(), time.Now, rand.Reader, alice)
	mustOK(t, err)
	opened, err := invitations.Open()
	mustOK(t, err)
	inviter, err := peer.NewInviter(pake.New(), invitations, c.alice)
	mustOK(t, err)
	joiner, err := peer.NewJoiner(pake.New(), bob, c.bob, time.Now, rand.Reader)
	mustOK(t, err)
	ctx := testContext(t)
	done := make(chan error, 1)
	go func() { _, err := inviter.Serve(ctx, c.server.Frames(ctx), c.server.Connection()); done <- err }()
	row, err := joiner.Join(ctx, c.client.Frames(ctx), peer.JoinInput{Connection: c.client.Connection(), Code: []byte(opened.Code), Name: "Bob"})
	mustOK(t, err)
	mustOK(t, <-done)
	return pairingWireStack{inviter, joiner, alice, bob, row}
}

func (s pairingWireStack) settle(t *testing.T, c pairingTLSConnections) {
	t.Helper()
	ctx := testContext(t)
	_, err := s.alice.Accept(ctx, c.bob.Fingerprint())
	mustOK(t, err)
	a, b := pipe(t)
	incoming := make(chan connResult, 1)
	go func() {
		conn, e := firstcontact.ServerWith(ctx, a, c.alice, time.Now, firstcontact.ServerOptions{ContactPins: func(fp string) bool { return fp == c.bob.Fingerprint() }})
		incoming <- connResult{conn, e}
	}()
	client, err := firstcontact.ContactClient(ctx, b, c.bob, time.Now, c.alice.Fingerprint())
	mustOK(t, err)
	server := <-incoming
	mustOK(t, server.err)
	done := make(chan error, 1)
	go func() { done <- s.inviter.ServeSettle(ctx, server.conn.Frames(ctx), server.conn.Connection()) }()
	row, err := s.joiner.Settle(ctx, client.Frames(ctx), client.Connection(), s.joined.OperationID)
	mustOK(t, err)
	mustOK(t, <-done)
	equal(t, row.State, peer.JoinContact)
	if row.DecisionConfirmedAt.IsZero() {
		t.Fatal("real settlement did not confirm the accepted join")
	}
}

func (s pairingWireStack) ledgerModes(t *testing.T, want peer.ExchangeMode) {
	t.Helper()
	ctx := context.Background()
	for db, op := range map[*store.Store]string{s.alice: s.joined.InviterOperationID, s.bob: s.joined.OperationID} {
		x, found, err := db.Exchange(ctx, op)
		mustOK(t, err)
		equal(t, found, true)
		equal(t, x.Mode, want)
		state := "legacy"
		if want == peer.ModeEpoch {
			state = "open"
		}
		equal(t, x.State, state)
	}
}

func TestPairingWireModeAcceptanceTLSModeSurvivesRealCPaceAndSettlement(t *testing.T) {
	for _, kind := range []string{"epoch", "v8-client", "v8-server"} {
		t.Run(kind, func(t *testing.T) {
			opts := pairingTLSOptions{}
			want := peer.ModeEpoch
			if kind == "v8-client" {
				opts.clientProtocols = []string{firstcontact.ALPN}
				want = peer.ModeLegacy
			}
			if kind == "v8-server" {
				opts.serverProtocols = []string{firstcontact.ALPN}
				want = peer.ModeLegacy
			}
			conns := pairingProtocolEndpoints(t, opts)
			stack := pairingWireJoin(t, conns)
			stack.ledgerModes(t, want)
			stack.settle(t, conns)
			for db, key := range map[*store.Store]string{stack.alice: conns.bob.Fingerprint(), stack.bob: conns.alice.Fingerprint()} {
				confirmed, err := db.PeerMessages().Confirmed(context.Background(), key)
				mustOK(t, err)
				equal(t, confirmed, true)
			}
			if want == peer.ModeEpoch {
				a, _, err := stack.alice.Exchange(context.Background(), stack.joined.InviterOperationID)
				mustOK(t, err)
				b, _, err := stack.bob.Exchange(context.Background(), stack.joined.OperationID)
				mustOK(t, err)
				equal(t, bytes.Equal(peer.PairingID(a.TranscriptID), peer.PairingID(b.TranscriptID)), true)
				equal(t, a.InstalledEpoch, int64(1))
				equal(t, b.InstalledEpoch, int64(1))
			}
		})
	}
}
