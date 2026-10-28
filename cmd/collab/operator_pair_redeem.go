package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorRedeemUsage = "usage: pairtask operator pair redeem DESTINATION [--name NAME] [--wait 45s] [--socket PATH] [--json] [--timeout 5s]\n" +
	"       the code is read from the terminal, or from stdin when piped; never pass it as an argument"

// redeemPoll is how often redeem checks the redemption's state.
const redeemPoll = 500 * time.Millisecond

// maxCodeLine bounds the code line read from stdin.
const maxCodeLine = 128

type redeemArgs struct {
	call        operatorCall
	destination string
	name        string
	wait        time.Duration
}

// runOperatorPairRedeem redeems another workspace's code (#178): it checks
// the broker can redeem before reading the code, sends it once, then follows
// that exact redemption until it settles or --wait ends. The code is never
// printed.
func runOperatorPairRedeem(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	a, code := parseRedeem(args, stderr)
	if code != -1 {
		return code
	}
	if !redeemSupported(ctx, a.call, stderr) {
		return 1
	}
	req, err := redeemRequest(a, stderr)
	if err != nil {
		return 1
	}
	resp, err := a.call.expect(ctx, req, expectation{protocol.TypeOperatorPairingResult, "operator pair redeem"}, stderr)
	if err != nil || resp.PairingJoin == nil {
		return 1
	}
	fmt.Fprintf(stderr, "redemption %q to %q recorded\n", resp.PairingJoin.OperationID, resp.PairingJoin.Destination)
	join := followRedeem(ctx, a, *resp.PairingJoin, stderr)
	return reportRedeem(stdout, stderr, a.call.asJSON, join)
}

func parseRedeem(args []string, stderr io.Writer) (redeemArgs, int) {
	var a redeemArgs
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, operatorRedeemUsage)
		return a, 2
	}
	a.destination = args[0]
	flags := flag.NewFlagSet("operator pair redeem", flag.ContinueOnError)
	a.call.register(flags)
	flags.StringVar(&a.name, "name", "", "the name to pair under (default: the broker's)")
	flags.DurationVar(&a.wait, "wait", 45*time.Second, "how long to follow the redemption")
	code := parseFlags(flags, args[1:], operatorRedeemUsage, stderr, func(rest []string) error {
		if len(rest) != 0 {
			return errors.New("pair redeem takes one destination; the code is read from stdin")
		}
		return a.call.validate()
	})
	return a, code
}

// redeemSupported asks before any code is read: an older broker, or one
// without redemption wired, is refused locally.
func redeemSupported(ctx context.Context, call operatorCall, stderr io.Writer) bool {
	probe := protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: uuid.NewString(), PairingOp: protocol.PairingOpStatus}
	resp, err := call.expect(ctx, probe, expectation{protocol.TypeOperatorPairingResult, "operator pair status"}, stderr)
	if err != nil {
		return false
	}
	if resp.PairingResult == nil || !resp.PairingResult.RedeemAvailable {
		fmt.Fprintln(stderr, "this broker cannot redeem codes: it is older, or redemption is not configured")
		return false
	}
	return true
}

// redeemRequest reads the code and checks the whole request locally.
func redeemRequest(a redeemArgs, stderr io.Writer) (protocol.Message, error) {
	code, err := readCode(stderr)
	if err != nil {
		fmt.Fprintln(stderr, "operator pair redeem: no code read")
		return protocol.Message{}, err
	}
	id := uuid.NewString()
	check := peer.JoinRequest{OperationID: id, Destination: a.destination, Name: a.name, Code: []byte(code)}
	if check.Name == "" {
		check.Name = "workspace" // the broker's own default is checked there
	}
	if err := peer.CheckJoinRequest(check); err != nil {
		fmt.Fprintln(stderr, "operator pair redeem: the code must be 4 to 18 digits, the destination host:port and the name at most 64 printable bytes; nothing sent")
		return protocol.Message{}, err
	}
	return protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: id, PairingOp: protocol.PairingOpRedeem,
		PairingDestination: a.destination, PairingCode: code, PairingName: a.name}, nil
}

// readCode reads one line: prompting on a terminal, plainly from a pipe.
func readCode(stderr io.Writer) (string, error) {
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprint(stderr, "Code from the other operator: ")
	}
	// One byte past the bound shows overlong input, refused rather than cut.
	line, err := bufio.NewReader(io.LimitReader(os.Stdin, maxCodeLine+1)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if len(line) > maxCodeLine {
		return "", errors.New("code line too long")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", io.ErrUnexpectedEOF
	}
	return line, nil
}

// followRedeem polls the exact redemption until it leaves intent and
// exchanging, or until --wait ends.
func followRedeem(ctx context.Context, a redeemArgs, join protocol.PairingJoinView, stderr io.Writer) protocol.PairingJoinView {
	// --wait bounds the sleeps and the polls alike.
	ctx, cancel := context.WithTimeout(ctx, a.wait)
	defer cancel()
	for unsettled(join.State) {
		select {
		case <-ctx.Done():
			return join
		case <-time.After(redeemPoll):
		}
		poll := protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: uuid.NewString(), PairingOp: protocol.PairingOpStatus, PairingTarget: join.OperationID}
		resp, err := a.call.expect(ctx, poll, expectation{protocol.TypeOperatorPairingResult, "operator pair status"}, stderr)
		if err != nil || resp.PairingJoin == nil {
			return join
		}
		join = *resp.PairingJoin
	}
	return join
}

func unsettled(state string) bool {
	return state == string(peer.JoinIntent) || state == string(peer.JoinExchanging)
}

// redeemStates explain each settled state; awaiting_acceptance is not yet a
// contact.
var redeemStates = map[string]string{
	string(peer.JoinAwaitingAcceptance): "the inviter recorded it; it awaits their acceptance",
	string(peer.JoinOutcomeUnknown):     "our final confirmation may have arrived, but no receipt came; it awaits reconciliation",
	string(peer.JoinFailed):             "it failed before the inviter could record it",
	string(peer.JoinUnverified):         "it was interrupted before the inviter was verified",
}

func reportRedeem(stdout, stderr io.Writer, asJSON bool, join protocol.PairingJoinView) int {
	if asJSON {
		if code := encodeJSON(stdout, stderr, join); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(stdout, "redemption %q: %s\n", join.OperationID, join.State)
	}
	if join.State == string(peer.JoinAwaitingAcceptance) {
		fmt.Fprintln(stderr, redeemStates[join.State])
		return 0
	}
	explanation, ok := redeemStates[join.State]
	if !ok {
		explanation = fmt.Sprintf("still exchanging; check later with: pairtask operator pair status %q", join.OperationID)
	}
	fmt.Fprintln(stderr, explanation)
	return 1
}
