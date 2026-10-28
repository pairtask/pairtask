//go:build linux

package server

import (
	"bytes"
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pairtask/pairtask/internal/peer"
)

func TestRedeemOperatorAcceptanceTwoBrokersThroughOpaqueRelay(t *testing.T) {
	alice, bob := newRedeemOperator(t, redeemConfig(), ""), newRedeemOperator(t, redeemConfig(), "")
	code := pairingOpen(alice.operatorFixture).Code
	destination := relayToPairing(t, alice)
	reply := redeemCommand(bob, "two-brokers", destination, code)
	view := redeemReply(t, reply)
	operatorEqual(t, "stable operation ID", view.OperationID, "two-brokers")
	row := awaitRedeemState(t, bob, view.OperationID, peer.JoinAwaitingAcceptance)
	aliceFP := pairingSnapshot(t, pairingCommand(alice.operatorFixture, "status", "")).SelfIdentity
	operatorEqual(t, "authenticated inviter", row.Peer, aliceFP)
	operatorEqual(t, "stored destination", row.Destination, destination)
	requests, err := alice.st.Requests(context.Background())
	operatorNoError(t, err)
	operatorEqual(t, "one pending request", len(requests), 1)
	operatorEqual(t, "never auto accepted", requests[0].State, peer.PairingPending)
	operatorEqual(t, "actual Bob key", requests[0].IdentityKey, row.Joiner)
	outcome, found, err := alice.st.Outcome(context.Background(), row.InviterOperationID)
	operatorNoError(t, err)
	operatorEqual(t, "durable Alice receipt", found, true)
	operatorEqual(t, "same transcript", bytes.Equal(outcome.TranscriptID, row.TranscriptID), true)
	for _, f := range []*redeemOperatorFixture{alice, bob} {
		pairingNoCode(t, pairingCommand(f.operatorFixture, "status", ""), code)
		operatorEqual(t, "code absent from logs", strings.Contains(f.logs.String(), code), false)
		for _, table := range []string{"messages", "durable_inbox", "operator_requests"} {
			operatorEqual(t, "no message or agent authority in "+table, f.count(table), 0)
		}
	}
	pairingNoCode(t, reply, code)
}

func TestRedeemOperatorAcceptanceIntentReplayBusyAndResponsiveHub(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	cfg := redeemConfig()
	cfg.Dial = func(ctx context.Context, _ string) (net.Conn, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return nil, errors.New("unreachable")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := newRedeemOperator(t, cfg, "")
	first := redeemReply(t, redeemCommand(f, "stable", "host:443", "739-281"))
	operatorEqual(t, "intent acknowledged", first.State, "intent")
	waitRedeemStarted(t, started)
	operatorEqual(t, "persisted before dial completes", awaitRedeemState(t, f, "stable", peer.JoinIntent).Destination, "host:443")
	replay := redeemReply(t, redeemCommand(f, "stable", "host:443", "111111"))
	operatorEqual(t, "replay keeps operation", replay.OperationID, first.OperationID)
	operatorEqual(t, "one dial", calls.Load(), int32(1))
	conflict := redeemCommand(f, "stable", "other:443", "739-281")
	operatorEqual(t, "changed destination refused", conflict.text("type"), "error")
	busy := redeemCommand(f, "second", "host:443", "739-281")
	operatorEqual(t, "busy refusal", busy.text("type"), "error")
	operatorEqual(t, "no second intent", f.count("peer_joins"), 1)
	pairingSnapshot(t, pairingCommand(f.operatorFixture, "status", ""))
	close(release)
	awaitRedeemState(t, f, "stable", peer.JoinFailed)
}

func TestRedeemOperatorAcceptanceShutdownSettlesSQLiteAndReplaySurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shutdown.db")
	started := make(chan struct{}, 1)
	cfg := redeemConfig()
	cfg.Dial = func(ctx context.Context, _ string) (net.Conn, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f := newRedeemOperator(t, cfg, path)
	redeemReply(t, redeemCommand(f, "shutdown", "host:443", "739-281"))
	waitRedeemStarted(t, started)
	f.stop()
	var calls atomic.Int32
	cfg.Dial = func(context.Context, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("must not dial replay")
	}
	restarted := newRedeemOperator(t, cfg, path)
	row := awaitRedeemState(t, restarted, "shutdown", peer.JoinFailed)
	operatorEqual(t, "original destination persisted", row.Destination, "host:443")
	replay := redeemReply(t, redeemCommand(restarted, "shutdown", "host:443", "111111"))
	operatorEqual(t, "failure remains settled", replay.State, "failed")
	operatorEqual(t, "replay never dials", calls.Load(), int32(0))
	operatorEqual(t, "one durable operation", restarted.count("peer_joins"), 1)
}

func TestRedeemOperatorAcceptanceUnavailableAndInvalidInputsNeverDial(t *testing.T) {
	cfg := redeemConfig()
	cfg.Dial = nil
	f := newRedeemOperator(t, cfg, "")
	operatorEqual(t, "unavailable refusal", redeemCommand(f, "off", "host:443", "739-281").text("type"), "error")
	operatorEqual(t, "unavailable creates no intent", f.count("peer_joins"), 0)
	var calls atomic.Int32
	cfg = redeemConfig()
	cfg.Dial = func(context.Context, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("should not dial")
	}
	f = newRedeemOperator(t, cfg, "")
	for _, destination := range []string{"", ":443", "host:0", "host:65536", "host", "host\n:443", strings.Repeat("x", 256) + ":443"} {
		operatorEqual(t, "invalid destination refused", redeemCommand(f, "invalid", destination, "739-281").text("type"), "error")
	}
	for _, code := range []string{"", "abc", strings.Repeat("7", 65)} {
		operatorEqual(t, "invalid code refused", redeemCommand(f, "invalid", "host:443", code).text("type"), "error")
	}
	operatorEqual(t, "invalid requests create no intent", f.count("peer_joins"), 0)
	operatorEqual(t, "invalid requests never dial", calls.Load(), int32(0))
}
