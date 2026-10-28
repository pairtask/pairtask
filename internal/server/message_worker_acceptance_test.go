//go:build linux

package server

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/peer/messaging"
)

func messageJoinedContact(f *redeemOperatorFixture, remote, destination string) {
	f.t.Helper()
	ctx := context.Background()
	at := time.Now()
	ep, err := f.h.PairingEndpoint()
	operatorNoError(f.t, err)
	_, err = f.st.BeginJoin(ctx, peer.JoinStart{OperationID: "message-join", Joiner: ep.Identity.Fingerprint(), Name: "Local", Destination: destination, At: at})
	operatorNoError(f.t, err)
	_, err = f.st.SetCorrelation(ctx, "message-join", peer.InviterHello{InvitationID: "invitation", OperationID: "inviter-operation"}, at)
	operatorNoError(f.t, err)
	row, err := f.st.RecordVerified(ctx, "message-join", peer.ExchangeResult{Peer: remote, TranscriptID: make([]byte, 64)}, at)
	operatorNoError(f.t, err)
	_, err = f.st.RecordReceipt(ctx, "message-join", peer.JoinReceipt{InvitationID: row.InvitationID, InviterOperationID: row.InviterOperationID, Joiner: row.Joiner, TranscriptID: row.TranscriptID}, at)
	operatorNoError(f.t, err)
	_, err = f.st.RecordDecision(ctx, "message-join", peer.PairingAccepted, at)
	operatorNoError(f.t, err)
	_, err = f.st.RecordDecisionConfirmed(ctx, "message-join", peer.PairingAccepted, at)
	operatorNoError(f.t, err)
}

func awaitMessageDelivery(t *testing.T, s messaging.Store, contact, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows, _, err := s.Page(context.Background(), contact, 0, 64)
		operatorNoError(t, err)
		for _, row := range rows {
			if row.ID == id && row.Direction == messaging.DirOut && row.State == messaging.StateDelivered {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("message %s not delivered: %+v", id, rows)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMessageWorkerAcceptanceInviterWithoutAddressSendsOnJoinerSync(t *testing.T) {
	var reverseDials atomic.Int32
	cfg := redeemConfig()
	cfg.Dial = func(context.Context, string) (net.Conn, error) {
		reverseDials.Add(1)
		return nil, errors.New("inviter must not dial")
	}
	inviter := newRedeemOperator(t, cfg, "")
	joiner := newRedeemOperator(t, redeemConfig(), "")
	inviterEP, err := inviter.h.PairingEndpoint()
	operatorNoError(t, err)
	joinerEP, err := joiner.h.PairingEndpoint()
	operatorNoError(t, err)
	messageConfirm(inviter, joinerEP.Identity, 1)
	messageJoinedContact(joiner, inviterEP.Identity.Fingerprint(), relayToPairing(t, inviter))
	ctx := context.Background()
	_, err = inviter.st.PeerMessages().Queue(ctx, joinerEP.Identity.Fingerprint(), "from-inviter", "reply without reverse address", time.Now())
	operatorNoError(t, err)
	_, err = joiner.st.PeerMessages().Queue(ctx, inviterEP.Identity.Fingerprint(), "from-joiner", "hello", time.Now())
	operatorNoError(t, err)
	joiner.h.SyncMessages(inviterEP.Identity.Fingerprint())
	awaitMessageDelivery(t, joiner.st.PeerMessages(), inviterEP.Identity.Fingerprint(), "from-joiner")
	awaitMessageDelivery(t, inviter.st.PeerMessages(), joinerEP.Identity.Fingerprint(), "from-inviter")
	operatorEqual(t, "inviter never reverse dials", reverseDials.Load(), int32(0))
	for _, f := range []*redeemOperatorFixture{inviter, joiner} {
		operatorEqual(t, "contact traffic not local messages", f.count("messages"), 0)
		operatorEqual(t, "contact traffic not agent inbox", f.count("durable_inbox"), 0)
	}
}
