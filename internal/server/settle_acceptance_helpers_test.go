//go:build linux

package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

func settleConfig() hub.PairingConfig {
	cfg := redeemConfig()
	cfg.SettleTick = make(chan time.Time)
	return cfg
}

func settleSnapshot(t *testing.T, frame sessionWire) protocol.PairingSnapshot {
	t.Helper()
	operatorEqual(t, "settlement response: "+sessionJSON(frame), frame.text("type"), "operator_pairing_result")
	return sessionDecode[protocol.PairingSnapshot](t, frame, "pairing_result")
}

func settleSchedule(t *testing.T, f *redeemOperatorFixture, id string) protocol.PairingSettleView {
	t.Helper()
	frame := pairingCommand(f.operatorFixture, "settle", id)
	settleSnapshot(t, frame)
	return sessionDecode[protocol.PairingSettleView](t, frame, "pairing_settle")
}

type settleSeed struct {
	id, peer, destination string
	at                    time.Time
	decision              peer.PairingState
	confirmed             bool
}

func seedSettleJoin(t *testing.T, f *redeemOperatorFixture, seed settleSeed) peer.JoinRecord {
	t.Helper()
	ctx := context.Background()
	self := pairingSnapshot(t, pairingCommand(f.operatorFixture, "status", "")).SelfIdentity
	_, err := f.st.BeginJoin(ctx, peer.JoinStart{OperationID: seed.id, Joiner: self, Destination: seed.destination, At: seed.at})
	operatorNoError(t, err)
	_, err = f.st.SetCorrelation(ctx, seed.id, peer.InviterHello{InvitationID: "inv-" + seed.id, OperationID: "remote-" + seed.id}, seed.at)
	operatorNoError(t, err)
	r, err := f.st.RecordVerified(ctx, seed.id, peer.ExchangeResult{Peer: seed.peer, TranscriptID: []byte("test transcript")}, seed.at)
	operatorNoError(t, err)
	r, err = f.st.RecordReceipt(ctx, seed.id, peer.JoinReceipt{InvitationID: r.InvitationID, InviterOperationID: r.InviterOperationID, Joiner: r.Joiner, TranscriptID: r.TranscriptID}, seed.at)
	operatorNoError(t, err)
	if seed.decision != "" {
		r, err = f.st.RecordDecision(ctx, seed.id, seed.decision, seed.at)
		operatorNoError(t, err)
	}
	if seed.confirmed {
		r, err = f.st.RecordDecisionConfirmed(ctx, seed.id, seed.decision, seed.at)
		operatorNoError(t, err)
	}
	return r
}

func settleIdentity(n int) string { return fmt.Sprintf("sha256:%064x", n) }

func awaitSettle(t *testing.T, check func() bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for !check() {
		if time.Now().After(until) {
			t.Fatal("settlement condition was not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func awaitSettleConfirmed(t *testing.T, f *redeemOperatorFixture, id string) peer.JoinRecord {
	t.Helper()
	var r peer.JoinRecord
	awaitSettle(t, func() bool {
		var err error
		r, _, err = f.st.GetJoin(context.Background(), id)
		operatorNoError(t, err)
		return !r.DecisionConfirmedAt.IsZero()
	})
	return r
}

func settleContact(t *testing.T, snap protocol.PairingSnapshot, identity string) protocol.PairingContactView {
	t.Helper()
	for _, c := range snap.Contacts {
		if c.IdentityKey == identity {
			return c
		}
	}
	t.Fatalf("contact %s missing from %+v", identity, snap.Contacts)
	return protocol.PairingContactView{}
}

func settleSeedPair(t *testing.T, alice, bob *redeemOperatorFixture) peer.JoinRecord {
	t.Helper()
	key := pairingSnapshot(t, pairingCommand(alice.operatorFixture, "status", "")).SelfIdentity
	r := seedSettleJoin(t, bob, settleSeed{id: "notify", peer: key, destination: relayToPairing(t, alice), at: time.Now()})
	_, err := alice.st.Commit(context.Background(), peer.PairingOutcome{OperationID: r.InviterOperationID, InvitationID: r.InvitationID,
		IdentityKey: r.Joiner, Name: "Bob", TranscriptID: r.TranscriptID, At: time.Now()})
	operatorNoError(t, err)
	_, err = alice.st.Accept(context.Background(), r.Joiner)
	operatorNoError(t, err)
	return r
}

func settleEvent(t *testing.T, session *sessionProbe) {
	t.Helper()
	frame := session.frame()
	operatorEqual(t, "operator notification", frame.text("type"), "event")
	ev := sessionDecode[protocol.SessionEvent](t, frame, "event")
	operatorEqual(t, "pairing event kind", ev.Kind, "pairing_changed")
	operatorEqual(t, "event has no peer metadata", ev.AgentID+ev.MessageID+ev.Detail, "")
}
