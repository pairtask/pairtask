//go:build linux

package server

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestControlFallbackAcceptanceRefusedMessageDialLearnsNoAddressPause(t *testing.T) {
	cfg := redeemConfig()
	cfg.MessageTick = make(chan time.Time)
	inviter := newRedeemOperator(t, cfg, "")
	var calls atomic.Int32
	cfg = redeemConfig()
	cfg.MessageTick = make(chan time.Time)
	cfg.Dial = func(ctx context.Context, destination string) (net.Conn, error) {
		calls.Add(1)
		return (&net.Dialer{}).DialContext(ctx, "tcp", destination)
	}
	dialer := newRedeemOperator(t, cfg, "")
	a, err := inviter.h.PairingEndpoint()
	operatorNoError(t, err)
	b, err := dialer.h.PairingEndpoint()
	operatorNoError(t, err)
	messageConfirm(inviter, b.Identity, 1)
	messageConfirm(dialer, a.Identity, 1)
	destination := relayToPairing(t, inviter)
	_, err = dialer.db.Exec(`UPDATE peer_contacts SET destination=?,joined=1,invited=0 WHERE identity_key=?`, destination, a.Identity.Fingerprint())
	operatorNoError(t, err)
	bound, err := inviter.st.PeerMessages().Admit(context.Background(), b.Identity.Fingerprint())
	operatorNoError(t, err)
	operatorNoError(t, bound.SawVersion(context.Background(), 9))
	pauseServerContact(t, inviter, b.Identity.Fingerprint(), true)
	reply := dialer.h.SubmitOperator(context.Background(), 0, protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpSend, ContactKey: a.Identity.Fingerprint(), ContactText: "never imported", MessageID: "refused-then-control"})
	operatorEqual(t, "outgoing queued", reply.Type, protocol.TypeOperatorContactResult)
	awaitFallbackPeerState(t, dialer, a.Identity.Fingerprint())
	operatorEqual(t, "one message dial and one control dial", calls.Load(), int32(2))
	operatorEqual(t, "no imported content at paused receiver", inviter.count("peer_messages"), 0)
}
func awaitFallbackPeerState(t *testing.T, f *redeemOperatorFixture, key string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var state string
		err := f.db.QueryRow(`SELECT peer_state FROM peer_contacts WHERE identity_key=?`, key).Scan(&state)
		if err == nil && state == "paused" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("refused ordinary dial never learned remote pause: state=%q err=%v", state, err)
		}
		time.Sleep(time.Millisecond)
	}
}
