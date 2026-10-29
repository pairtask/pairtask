//go:build linux

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/hub"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

func TestSettleOperatorAcceptanceTwoBrokersConfirmDecisionsAndRestart(t *testing.T) {
	for _, decision := range []string{"accept", "reject"} {
		t.Run(decision, func(t *testing.T) {
			alice := newRedeemOperator(t, settleConfig(), "")
			database := filepath.Join(t.TempDir(), "joiner.db")
			bob := newRedeemOperator(t, settleConfig(), database)
			code := pairingOpen(alice.operatorFixture).Code
			redeemReply(t, redeemCommand(bob, "roundtrip", relayToPairing(t, alice), code))
			row := awaitRedeemState(t, bob, "roundtrip", peer.JoinAwaitingAcceptance)
			before := settleSnapshot(t, pairingCommand(alice.operatorFixture, decision, row.Joiner))
			operatorEqual(t, "acceptance alone creates no inviter contact", len(before.Contacts), 0)
			operatorEqual(t, "decision not delivered yet", before.Requests[0].DeliveredAt == nil, true)
			operatorEqual(t, "one scheduled", settleSchedule(t, bob, row.OperationID).Queued, 1)
			confirmed := awaitSettleConfirmed(t, bob, row.OperationID)
			wantState, wantCount := peer.JoinContact, 1
			if decision == "reject" {
				wantState, wantCount = peer.JoinDeclined, 0
			}
			operatorEqual(t, "authenticated final decision", confirmed.State, wantState)
			for _, f := range []*redeemOperatorFixture{alice, bob} {
				snap := settleSnapshot(t, pairingCommand(f.operatorFixture, "status", ""))
				operatorEqual(t, "contact total", snap.ContactsTotal, wantCount)
				if wantCount == 1 {
					operatorEqual(t, "both sides confirmed", snap.Contacts[0].Confirmed, true)
				}
				pairingNoCode(t, snap, code)
				for _, table := range []string{"messages", "durable_inbox", "operator_requests"} {
					operatorEqual(t, "no ordinary messaging or agent grant", f.count(table), 0)
				}
			}
			request, _, err := alice.st.Request(context.Background(), row.Joiner)
			operatorNoError(t, err)
			operatorEqual(t, "inviter delivery durable", request.DeliveredAt.IsZero(), false)
			bob.stop()
			restarted := newRedeemOperator(t, settleConfig(), database)
			operatorEqual(t, "restart retains contacts", settleSnapshot(t, pairingCommand(restarted.operatorFixture, "status", "")).ContactsTotal, wantCount)
			operatorEqual(t, "restart retains confirmation", awaitSettleConfirmed(t, restarted, row.OperationID).DecisionConfirmedAt.Equal(confirmed.DecisionConfirmedAt), true)
		})
	}
}

func TestSettleOperatorAcceptanceAutomaticBackoffLimitAndManualOverride(t *testing.T) {
	tick := make(chan time.Time)
	var clock atomic.Int64
	start := time.Now()
	clock.Store(start.UnixNano())
	var calls atomic.Int32
	cfg := settleConfig()
	cfg.SettleTick, cfg.Clock = tick, func() time.Time { return time.Unix(0, clock.Load()) }
	cfg.Dial = func(context.Context, string) (net.Conn, error) {
		attempt := calls.Add(1)
		err := errors.New("remote secret diagnostic")
		// Alternate the reported cause so status distinguishes this completed
		// pass from the previous one. Starting Dial does not finish a pass.
		if attempt%2 == 0 {
			err = errors.Join(err, context.DeadlineExceeded)
		}
		return nil, err
	}
	f := newRedeemOperator(t, cfg, "")
	seedSettleJoin(t, f, settleSeed{id: "retry", peer: settleIdentity(1), destination: "host:443", at: start})
	seedSettleJoin(t, f, settleSeed{id: "old", peer: settleIdentity(1), destination: "host:443", at: start.Add(-hub.SettleWindow - time.Hour)})
	seedSettleJoin(t, f, settleSeed{id: "direct", peer: settleIdentity(1), at: start})
	seedSettleJoin(t, f, settleSeed{id: "done", peer: settleIdentity(1), destination: "host:443", at: start, decision: peer.PairingAccepted, confirmed: true})
	sendTick := func() {
		select {
		case tick <- cfg.Clock():
		case <-time.After(3 * time.Second):
			t.Fatal("settlement worker did not accept tick")
		}
	}
	sendTick()
	awaitSettleFailure(t, f, "retry", "other")
	operatorEqual(t, "only eligible join dialed", calls.Load(), int32(1))
	sendTick()
	time.Sleep(30 * time.Millisecond)
	operatorEqual(t, "backoff blocks same-time tick", calls.Load(), int32(1))
	for attempt := 2; attempt <= hub.SettleMaxAttempts; attempt++ {
		clock.Store(start.Add(time.Duration(attempt) * hub.SettleBackoffMax).UnixNano())
		sendTick()
		cause := "other"
		if attempt%2 == 0 {
			cause = "timeout"
		}
		// record updates the cause and backoff together. Keep this clock
		// fixed until that update, before advancing to the next attempt.
		awaitSettleFailure(t, f, "retry", cause)
		operatorEqual(t, "one dial per automatic pass", calls.Load(), int32(attempt))
	}
	clock.Store(start.Add(time.Duration(hub.SettleMaxAttempts+1) * hub.SettleBackoffMax).UnixNano())
	sendTick()
	time.Sleep(30 * time.Millisecond)
	operatorEqual(t, "automatic work bounded", calls.Load(), int32(hub.SettleMaxAttempts))
	clock.Store(start.Add(hub.SettleWindow + time.Hour).UnixNano())
	sendTick()
	time.Sleep(30 * time.Millisecond)
	operatorEqual(t, "old joins no longer retried automatically", calls.Load(), int32(hub.SettleMaxAttempts))
	settleSchedule(t, f, "retry")
	awaitSettleFailure(t, f, "retry", "other")
	operatorEqual(t, "manual override dials once", calls.Load(), int32(hub.SettleMaxAttempts+1))
	frame := pairingCommand(f.operatorFixture, "status", "retry")
	if strings.Contains(sessionJSON(frame)+f.logs.String(), "remote secret diagnostic") {
		t.Fatal("raw network diagnostic escaped")
	}
}

func TestSettleOperatorAcceptanceConfirmationUsesFullDurableHistory(t *testing.T) {
	f := newRedeemOperator(t, settleConfig(), "")
	at := time.Now().Add(-time.Hour)
	for i := range 20 {
		seedSettleJoin(t, f, settleSeed{id: fmt.Sprintf("history-%02d", i), peer: settleIdentity(i + 1), destination: "host:443", at: at.Add(time.Duration(i) * time.Second), decision: peer.PairingAccepted, confirmed: i != 0})
	}
	snap := settleSnapshot(t, pairingCommand(f.operatorFixture, "status", ""))
	operatorEqual(t, "joins are truncated", snap.JoinsTruncated, true)
	operatorEqual(t, "old omitted join remains confirming", settleContact(t, snap, settleIdentity(1)).Confirmed, false)
	_, err := f.st.RecordDecisionConfirmed(context.Background(), "history-00", peer.PairingAccepted, at.Add(time.Minute))
	operatorNoError(t, err)
	seedSettleJoin(t, f, settleSeed{id: "new-unconfirmed", peer: settleIdentity(1), destination: "host:443", at: time.Now(), decision: peer.PairingAccepted})
	snap = settleSnapshot(t, pairingCommand(f.operatorFixture, "status", ""))
	operatorEqual(t, "one contact per identity", snap.ContactsTotal, 20)
	operatorEqual(t, "another pending join cannot regress confirmed contact", settleContact(t, snap, settleIdentity(1)).Confirmed, true)
	// An inviter's delivered acceptance also confirms the same identity.
	pairingSeed(f.operatorFixture, settleIdentity(2), "accepted")
	_, err = f.st.Deliver(context.Background(), settleIdentity(2), peer.PairingAccepted, time.Now())
	operatorNoError(t, err)
	c := settleContact(t, settleSnapshot(t, pairingCommand(f.operatorFixture, "status", "")), settleIdentity(2))
	operatorEqual(t, "reciprocal directions retained", c.Joined && c.Invited && c.Confirmed, true)
}

func TestSettleOperatorAcceptanceContactListingBoundAndEmptyArray(t *testing.T) {
	f := newRedeemOperator(t, settleConfig(), "")
	empty := settleSnapshot(t, pairingCommand(f.operatorFixture, "status", ""))
	operatorEqual(t, "empty contacts are nonnull", empty.Contacts != nil, true)
	at := time.Now().Add(-time.Hour)
	for i := range protocol.ListedPairingContacts + 3 {
		seedSettleJoin(t, f, settleSeed{id: fmt.Sprintf("bounded-%02d", i), peer: settleIdentity(i + 1), destination: "host:443", at: at.Add(time.Duration(i) * time.Second), decision: peer.PairingAccepted})
	}
	snap := settleSnapshot(t, pairingCommand(f.operatorFixture, "status", ""))
	operatorEqual(t, "bounded contacts", len(snap.Contacts), protocol.ListedPairingContacts)
	operatorEqual(t, "full contact total", snap.ContactsTotal, protocol.ListedPairingContacts+3)
	operatorEqual(t, "truncation explicit", snap.ContactsTruncated, true)
	for i := 1; i < len(snap.Contacts); i++ {
		if snap.Contacts[i].EstablishedAt.After(snap.Contacts[i-1].EstablishedAt) {
			t.Fatal("contacts not newest first")
		}
	}
}

func TestSettleOperatorAcceptancePinnedMismatchPreservesStateAndRetryClearsError(t *testing.T) {
	alice, replacement := newRedeemOperator(t, settleConfig(), ""), newRedeemOperator(t, settleConfig(), "")
	good, wrong := relayToPairing(t, alice), relayToPairing(t, replacement)
	var redirect atomic.Bool
	cfg := settleConfig()
	cfg.Dial = func(ctx context.Context, destination string) (net.Conn, error) {
		if redirect.Load() {
			destination = wrong
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", destination)
	}
	bob := newRedeemOperator(t, cfg, "")
	redeemReply(t, redeemCommand(bob, "pin", good, pairingOpen(alice.operatorFixture).Code))
	row := awaitRedeemState(t, bob, "pin", peer.JoinAwaitingAcceptance)
	pairingSnapshot(t, pairingCommand(alice.operatorFixture, "accept", row.Joiner))
	// Permit Bob at the replacement's TLS boundary, so rejection must come
	// from Bob's existing server pin, not the unknown-client check.
	pairingSeed(replacement.operatorFixture, row.Joiner, "accepted")
	redirect.Store(true)
	settleSchedule(t, bob, "pin")
	awaitSettle(t, func() bool {
		frame := pairingCommand(bob.operatorFixture, "status", "pin")
		return sessionDecode[protocol.PairingJoinView](t, frame, "pairing_join").SettleError == "peer_mismatch"
	})
	operatorEqual(t, "pin mismatch cannot change decision", awaitRedeemState(t, bob, "pin", peer.JoinAwaitingAcceptance).Peer, row.Peer)
	operatorEqual(t, "pin mismatch creates no contact", settleSnapshot(t, pairingCommand(bob.operatorFixture, "status", "")).ContactsTotal, 0)
	redirect.Store(false)
	settleSchedule(t, bob, "pin")
	awaitSettleConfirmed(t, bob, "pin")
	awaitSettle(t, func() bool {
		return sessionDecode[protocol.PairingJoinView](t, pairingCommand(bob.operatorFixture, "status", "pin"), "pairing_join").SettleError == ""
	})
}

func TestSettleOperatorAcceptancePendingRepliesStillConsumeAutomaticBudget(t *testing.T) {
	alice := newRedeemOperator(t, settleConfig(), "")
	tick := make(chan time.Time)
	var calls atomic.Int32
	cfg := settleConfig()
	cfg.SettleTick = tick
	cfg.Dial = func(ctx context.Context, destination string) (net.Conn, error) {
		calls.Add(1)
		return (&net.Dialer{}).DialContext(ctx, "tcp", destination)
	}
	bob := newRedeemOperator(t, cfg, "")
	redeemReply(t, redeemCommand(bob, "pending", relayToPairing(t, alice), pairingOpen(alice.operatorFixture).Code))
	awaitRedeemState(t, bob, "pending", peer.JoinAwaitingAcceptance)
	for attempt := 1; attempt <= hub.SettleMaxAttempts; attempt++ {
		select {
		case tick <- time.Now():
		case <-time.After(3 * time.Second):
			t.Fatal("worker blocked")
		}
		awaitSettle(t, func() bool { return calls.Load() == int32(attempt+1) })
	}
	select {
	case tick <- time.Now():
	case <-time.After(3 * time.Second):
		t.Fatal("worker blocked")
	}
	time.Sleep(30 * time.Millisecond)
	operatorEqual(t, "pending replies do not reset attempt budget", calls.Load(), int32(hub.SettleMaxAttempts+1))
	operatorEqual(t, "pending creates no contact", settleSnapshot(t, pairingCommand(bob.operatorFixture, "status", "")).ContactsTotal, 0)
}

func TestSettleOperatorAcceptanceAuditAndAgentBoundaryBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	cfg := settleConfig()
	cfg.Dial = func(context.Context, string) (net.Conn, error) { calls.Add(1); return nil, errors.New("must not dial") }
	f := newRedeemOperator(t, cfg, "")
	seedSettleJoin(t, f, settleSeed{id: "audited", peer: settleIdentity(1), destination: "host:443", at: time.Now()})
	seedSettleJoin(t, f, settleSeed{id: "direct", peer: settleIdentity(1), at: time.Now()})
	before := f.count("operator_audit")
	for _, id := range []string{"missing", "direct"} {
		operatorEqual(t, "invalid target refused", pairingCommand(f.operatorFixture, "settle", id).text("type"), "error")
	}
	a, _ := roomPeer(f.operatorFixture, "worker", 9)
	response, _ := a.request("operator_pairing", map[string]any{"pairing_op": "settle", "pairing_target": "audited"})
	operatorEqual(t, "agent cannot settle", response.text("type"), "error")
	operatorEqual(t, "invalid requests never audited", f.count("operator_audit"), before)
	_, err := f.db.Exec(`CREATE TRIGGER deny_settle_audit BEFORE INSERT ON operator_audit BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	operatorNoError(t, err)
	operatorEqual(t, "audit failure refused", pairingCommand(f.operatorFixture, "settle", "audited").text("type"), "error")
	time.Sleep(30 * time.Millisecond)
	operatorEqual(t, "nothing dialed", calls.Load(), int32(0))
}

func TestSettleOperatorAcceptanceDeduplicatesActiveWorkAndReportsOverflow(t *testing.T) {
	started := make(chan struct{}, 2)
	cfg := settleConfig()
	cfg.Dial = func(ctx context.Context, _ string) (net.Conn, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f := newRedeemOperator(t, cfg, "")
	for i := range 70 {
		seedSettleJoin(t, f, settleSeed{id: fmt.Sprintf("queue-%02d", i), peer: settleIdentity(1), destination: "host:443", at: time.Now(), decision: peer.PairingAccepted})
	}
	operatorEqual(t, "first queued", settleSchedule(t, f, "queue-00").Queued, 1)
	waitRedeemStarted(t, started)
	for range 3 {
		view := settleSchedule(t, f, "queue-00")
		operatorEqual(t, "active operation not enqueued twice", view.Queued, 0)
		operatorEqual(t, "active operation reported", view.AlreadyQueued, 1)
	}
	view := settleSchedule(t, f, "")
	operatorEqual(t, "all candidates accounted for", view.Queued+view.AlreadyQueued+view.NotQueued, 70)
	if view.Queued > hub.SettleQueue || view.NotQueued == 0 {
		t.Fatalf("queue bound/overflow missing: %+v", view)
	}
	operatorEqual(t, "one worker only", len(started), 0)
	var action, scope string
	operatorNoError(t, f.db.QueryRow("SELECT action,scope FROM operator_audit ORDER BY id DESC LIMIT 1").Scan(&action, &scope))
	operatorEqual(t, "manual audit action", action, "pairing settle")
	operatorEqual(t, "manual all scope", scope, "all")
}

func TestSettleOperatorAcceptanceNotifiesBothOperatorsEvenAfterPartialWrite(t *testing.T) {
	for _, failConfirmation := range []bool{false, true} {
		t.Run(fmt.Sprintf("confirmation-failure-%v", failConfirmation), func(t *testing.T) {
			alice, bob := newRedeemOperator(t, settleConfig(), ""), newRedeemOperator(t, settleConfig(), "")
			r := settleSeedPair(t, alice, bob)
			agent, _ := roomPeer(alice.operatorFixture, "local-worker", 9)
			a, b := sessionOpen(alice.operatorFixture), sessionOpen(bob.operatorFixture)
			a.request("operator_snapshot", nil)
			b.request("operator_snapshot", nil)
			if failConfirmation {
				_, err := bob.db.Exec(`CREATE TRIGGER fail_settle_confirmation BEFORE UPDATE OF decision_confirmed_at ON peer_joins WHEN NEW.decision_confirmed_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'confirmation unavailable'); END`)
				operatorNoError(t, err)
			}
			settleSchedule(t, bob, r.OperationID)
			settleEvent(t, a)
			settleEvent(t, b)
			stored := awaitRedeemState(t, bob, r.OperationID, peer.JoinContact)
			operatorEqual(t, "notification follows durable decision", stored.DecisionConfirmedAt.IsZero(), failConfirmation)
			operatorEqual(t, "remote notification follows contact write", settleSnapshot(t, pairingCommand(alice.operatorFixture, "status", "")).ContactsTotal, 1)
			operatorNoError(t, agent.SetReadDeadline(time.Now().Add(30*time.Millisecond)))
			_, err := agent.r.ReadBytes('\n')
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("pairing notification reached agent: %v", err)
			}
		})
	}
}

func TestSettleOperatorAcceptancePinFailureNotifiesOpenOperator(t *testing.T) {
	cfg := settleConfig()
	cfg.Dial = func(context.Context, string) (net.Conn, error) { return nil, peer.ErrPeerMismatch }
	f := newRedeemOperator(t, cfg, "")
	seedSettleJoin(t, f, settleSeed{id: "changed-key", peer: settleIdentity(1), destination: "host:443", at: time.Now()})
	owner := sessionOpen(f.operatorFixture)
	owner.request("operator_snapshot", nil)
	settleSchedule(t, f, "changed-key")
	settleEvent(t, owner)
	frame := pairingCommand(f.operatorFixture, "status", "changed-key")
	row := sessionDecode[protocol.PairingJoinView](t, frame, "pairing_join")
	operatorEqual(t, "changed identity is visible without reconnect", row.SettleError, "peer_mismatch")
	operatorEqual(t, "failure did not invent a decision", row.State, "awaiting_acceptance")
}
