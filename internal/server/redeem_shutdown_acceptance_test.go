//go:build linux

package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/pake"
)

// The real PAKE authenticates the inviter and persists the result, then
// deliberately stalls before its final confirmation. Cancelling network
// work must preserve that already-verified result in SQLite.
type stallVerifiedExchange struct {
	peer.Exchanger
	verified chan struct{}
}

func (x stallVerifiedExchange) Join(ctx context.Context, f peer.Frames, b peer.ExchangeBinding, code []byte, persist func(context.Context, peer.ExchangeResult) error) error {
	return x.Exchanger.Join(ctx, f, b, code, func(ctx context.Context, r peer.ExchangeResult) error {
		if err := persist(ctx, r); err != nil {
			return err
		}
		x.verified <- struct{}{}
		_, err := f.Recv()
		return err
	})
}
func TestRedeemOperatorAcceptanceShutdownRetainsVerifiedUnknown(t *testing.T) {
	alice := newRedeemOperator(t, redeemConfig(), "")
	code := pairingOpen(alice.operatorFixture).Code
	address := relayToPairing(t, alice)
	path := filepath.Join(t.TempDir(), "verified.db")
	verified := make(chan struct{}, 1)
	cfg := redeemConfig()
	cfg.Exchanger = stallVerifiedExchange{Exchanger: pake.New(), verified: verified}
	bob := newRedeemOperator(t, cfg, path)
	redeemReply(t, redeemCommand(bob, "verified-shutdown", address, code))
	waitRedeemStarted(t, verified)
	before := awaitRedeemState(t, bob, "verified-shutdown", peer.JoinOutcomeUnknown)
	bob.stop()
	restarted := newRedeemOperator(t, redeemConfig(), path)
	after := awaitRedeemState(t, restarted, "verified-shutdown", peer.JoinOutcomeUnknown)
	operatorEqual(t, "verified identity survives", after.Peer, before.Peer)
	operatorEqual(t, "verified transcript survives", string(after.TranscriptID), string(before.TranscriptID))
	operatorEqual(t, "one operation after restart", restarted.count("peer_joins"), 1)
	operatorEqual(t, "no receipt falsely inferred", alice.count("peer_pairing_requests"), 0)
}
