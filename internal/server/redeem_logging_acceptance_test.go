//go:build linux

package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

// Provider errors are diagnostic text, not a safe logging boundary. A
// provider regression must not let the code reach either broker's logs.
type diagnosticExchange struct{}

func (diagnosticExchange) Join(_ context.Context, _ peer.Frames, _ peer.ExchangeBinding, code []byte, _ func(context.Context, peer.ExchangeResult) error) error {
	return fmt.Errorf("provider rejected secret %s", code)
}
func (diagnosticExchange) Invite(_ context.Context, _ peer.Frames, _ peer.ExchangeBinding, code []byte) (peer.ExchangeResult, error) {
	return peer.ExchangeResult{}, fmt.Errorf("provider rejected secret %s", code)
}
func TestRedeemOperatorAcceptanceProviderDiagnosticsCannotLeakCode(t *testing.T) {
	for _, side := range []string{"incoming", "outgoing"} {
		t.Run(side, func(t *testing.T) {
			aliceConfig, bobConfig := redeemConfig(), redeemConfig()
			if side == "incoming" {
				aliceConfig.Exchanger = diagnosticExchange{}
			} else {
				bobConfig.Exchanger = diagnosticExchange{}
			}
			alice, bob := newRedeemOperator(t, aliceConfig, ""), newRedeemOperator(t, bobConfig, "")
			code := pairingOpen(alice.operatorFixture).Code
			redeemReply(t, redeemCommand(bob, "diagnostic", relayToPairing(t, alice), code))
			awaitRedeemState(t, bob, "diagnostic", peer.JoinFailed)
			// Stop waits for both connection workers and their final logs.
			bob.stop()
			alice.stop()
			for _, log := range []string{alice.logs.String(), bob.logs.String()} {
				operatorEqual(t, "formatted code absent", strings.Contains(log, code), false)
				operatorEqual(t, "normalized code absent", strings.Contains(log, strings.ReplaceAll(code, "-", "")), false)
			}
		})
	}
}
