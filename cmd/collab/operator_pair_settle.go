package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorSettleUsage = "usage: pairtask operator pair settle [--wait 45s] [--socket PATH] [--json] [--timeout 5s] [OPERATION_ID]"

type settleArgs struct {
	call   operatorCall
	target string
	wait   time.Duration
}

// runOperatorPairSettle asks the broker to settle one redemption, or every
// one that still needs it (#178). Only a named redemption is followed, until
// its decision is confirmed on both sides or --wait ends.
func runOperatorPairSettle(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	a, code := parseSettle(args, stderr)
	if code != -1 {
		return code
	}
	if !settleSupported(ctx, a.call, stderr) {
		return 1
	}
	req := protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: uuid.NewString(), PairingOp: protocol.PairingOpSettle,
		PairingTarget: a.target}
	resp, err := a.call.expect(ctx, req, expectation{protocol.TypeOperatorPairingResult, "operator pair settle"}, stderr)
	if err != nil || resp.PairingSettle == nil {
		return 1
	}
	s := resp.PairingSettle
	fmt.Fprintf(stderr, "settlement queued here: %d, already queued: %d, not queued (queue full): %d\n", s.Queued, s.AlreadyQueued, s.NotQueued)
	if a.target == "" {
		return reportQueued(stdout, stderr, a.call.asJSON, resp)
	}
	if resp.PairingJoin == nil {
		fmt.Fprintf(stderr, "the broker did not report redemption %q; its settlement is not confirmed\n", a.target)
		return 1
	}
	join := followSettle(ctx, a, *resp.PairingJoin, stderr)
	return reportSettled(stdout, stderr, a.call.asJSON, join)
}

func parseSettle(args []string, stderr io.Writer) (settleArgs, int) {
	var a settleArgs
	flags := flag.NewFlagSet("operator pair settle", flag.ContinueOnError)
	a.call.register(flags)
	flags.DurationVar(&a.wait, "wait", 45*time.Second, "how long to follow a named settlement")
	leading, args := splitLeading(args)
	code := parseFlags(flags, args, operatorSettleUsage, stderr, func(rest []string) error {
		rest = append(leading, rest...)
		if len(rest) > 1 {
			return errors.New("pair settle takes at most one operation ID")
		}
		if len(rest) == 1 {
			if strings.TrimSpace(rest[0]) == "" {
				return errors.New("pair settle: the operation ID is empty")
			}
			a.target = rest[0]
		}
		return a.call.validate()
	})
	return a, code
}

// settleSupported refuses an older broker, or one that can't reach peers,
// before anything is queued.
func settleSupported(ctx context.Context, call operatorCall, stderr io.Writer) bool {
	probe := protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: uuid.NewString(), PairingOp: protocol.PairingOpStatus}
	resp, err := call.expect(ctx, probe, expectation{protocol.TypeOperatorPairingResult, "operator pair status"}, stderr)
	if err != nil {
		return false
	}
	if resp.PairingResult == nil || resp.PairingResult.Contacts == nil || !resp.PairingResult.RedeemAvailable {
		fmt.Fprintln(stderr, "this broker cannot settle pairings: it is older, or redemption is not configured")
		return false
	}
	return true
}

func reportQueued(stdout, stderr io.Writer, asJSON bool, resp protocol.Message) int {
	if asJSON {
		return encodeJSON(stdout, stderr, resp.PairingSettle)
	}
	fmt.Fprintln(stdout, "queued locally; pairtask operator pair status shows progress")
	return 0
}

// followSettle polls the named redemption until it is confirmed, its
// settlement error changes, or --wait ends; --wait bounds the polls too.
func followSettle(ctx context.Context, a settleArgs, join protocol.PairingJoinView, stderr io.Writer) protocol.PairingJoinView {
	ctx, cancel := context.WithTimeout(ctx, a.wait)
	defer cancel()
	firstError := join.SettleError
	for !settleDone(join, firstError) {
		select {
		case <-ctx.Done():
			return join
		case <-time.After(redeemPoll):
		}
		poll := protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: uuid.NewString(), PairingOp: protocol.PairingOpStatus,
			PairingTarget: join.OperationID}
		resp, err := a.call.expect(ctx, poll, expectation{protocol.TypeOperatorPairingResult, "operator pair status"}, stderr)
		if err != nil || resp.PairingJoin == nil {
			return join
		}
		join = *resp.PairingJoin
	}
	return join
}

func settleDone(join protocol.PairingJoinView, firstError string) bool {
	return join.DecisionConfirmedAt != nil || join.SettleError != firstError
}

// settleStates explain where a settled redemption stands.
var settleStates = map[string]string{
	string(peer.JoinContact):            "the inviter accepted; this workspace is now its contact",
	string(peer.JoinDeclined):           "the inviter declined",
	string(peer.JoinAwaitingAcceptance): "the inviter has not decided yet",
	string(peer.JoinOutcomeUnknown):     "the inviter could not yet be reached to confirm the receipt",
}

func reportSettled(stdout, stderr io.Writer, asJSON bool, join protocol.PairingJoinView) int {
	if asJSON {
		if code := encodeJSON(stdout, stderr, join); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(stdout, "redemption %q: %s%s\n", join.OperationID, join.State, confirmation(join))
	}
	if explanation, ok := settleStates[join.State]; ok {
		fmt.Fprintln(stderr, explanation)
	}
	if join.SettleError != "" {
		fmt.Fprintf(stderr, "last settlement: %q\n", join.SettleError)
	}
	if join.DecisionConfirmedAt != nil {
		return 0
	}
	return 1
}

// confirmation labels a decided join honestly.
func confirmation(join protocol.PairingJoinView) string {
	if join.State != string(peer.JoinContact) && join.State != string(peer.JoinDeclined) {
		return ""
	}
	if join.DecisionConfirmedAt == nil {
		return " (confirming)"
	}
	return " (confirmed)"
}
