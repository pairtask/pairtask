package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
)

const operatorPairUsage = "usage: pairtask operator pair [list | open | status [OPERATION_ID] | cancel [INVITATION_ID] | accept KEY | reject KEY] [--socket PATH] [--json] [--timeout 5s]\n       pairtask operator pair redeem DESTINATION [--name NAME] [--wait 45s] (the code is read from stdin)"

// pairVerbs are the pairing operations, how many positional arguments each
// takes, and the read that resolves a missing or prefixed target first.
var pairVerbs = map[string]struct {
	args []int
	read string
}{
	protocol.PairingOpList:   {[]int{0}, ""},
	protocol.PairingOpOpen:   {[]int{0}, ""},
	protocol.PairingOpStatus: {[]int{0, 1}, ""}, // optional: one redemption's operation ID
	protocol.PairingOpCancel: {[]int{0, 1}, protocol.PairingOpStatus},
	protocol.PairingOpAccept: {[]int{1}, protocol.PairingOpList},
	protocol.PairingOpReject: {[]int{1}, protocol.PairingOpList},
}

// pairTargets resolve a read into the full target a mutation needs.
var pairTargets = map[string]func(s *protocol.PairingSnapshot, typed string) (string, error){
	protocol.PairingOpCancel: func(s *protocol.PairingSnapshot, _ string) (string, error) { return s.InvitationID() },
	protocol.PairingOpAccept: (*protocol.PairingSnapshot).ResolveKey,
	protocol.PairingOpReject: (*protocol.PairingSnapshot).ResolveKey,
}

// runOperatorPair manages pairing invitations and requests (#178). Only open
// prints the code, on stdout.
func runOperatorPair(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == protocol.PairingOpRedeem {
		return runOperatorPairRedeem(ctx, args[1:], stdout, stderr)
	}
	call, req, code := parseOperatorPair(args, stderr)
	if code != -1 {
		return code
	}
	if resolvePairTarget(ctx, call, &req, stderr) != nil {
		return 1
	}
	resp, err := call.expect(ctx, req, expectation{protocol.TypeOperatorPairingResult, "operator pair " + req.PairingOp}, stderr)
	if err != nil {
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, resp)
	}
	writeJoin(stdout, resp.PairingJoin)
	writePairing(stdout, resp.PairingResult)
	return 0
}

// writeJoin prints the redemption a targeted status named.
func writeJoin(w io.Writer, j *protocol.PairingJoinView) {
	if j == nil {
		return
	}
	fmt.Fprintf(w, "Redemption %q to %q: %s\n", j.OperationID, j.Destination, j.State)
}

func parseOperatorPair(args []string, stderr io.Writer) (operatorCall, protocol.Message, int) {
	var call operatorCall
	op := protocol.PairingOpList
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		op, args = args[0], args[1:]
	}
	verb, ok := pairVerbs[op]
	if !ok {
		fmt.Fprintln(stderr, operatorPairUsage)
		return call, protocol.Message{}, 2
	}
	req := protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: uuid.NewString(), PairingOp: op}
	flags := flag.NewFlagSet("operator pair", flag.ContinueOnError)
	call.register(flags)
	// Positional arguments may come before or after the flags.
	leading, args := splitLeading(args)
	code := parseFlags(flags, args, operatorPairUsage, stderr, func(rest []string) error {
		rest = append(leading, rest...)
		if !slices.Contains(verb.args, len(rest)) {
			return fmt.Errorf("pair %s: wrong number of arguments", op)
		}
		if len(rest) == 1 {
			if strings.TrimSpace(rest[0]) == "" {
				return fmt.Errorf("pair %s: the argument is empty", op)
			}
			req.PairingTarget = rest[0]
		}
		return call.validate()
	})
	return call, req, code
}

// splitLeading separates the arguments before the first flag.
func splitLeading(args []string) (leading, rest []string) {
	i := slices.IndexFunc(args, func(a string) bool { return strings.HasPrefix(a, "-") })
	if i < 0 {
		return args, nil
	}
	return args[:i], args[i:]
}

// resolvePairTarget fills the full target before a mutation: cancel without
// an ID reads the open invitation; accept and reject resolve a key prefix
// against a listing, and nothing is sent unless it names exactly one request.
func resolvePairTarget(ctx context.Context, call operatorCall, req *protocol.Message, stderr io.Writer) error {
	read := pairRead(*req)
	if read == "" {
		return nil
	}
	resp, err := call.expect(ctx, protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: uuid.NewString(), PairingOp: read},
		expectation{protocol.TypeOperatorPairingResult, "operator pair " + read}, stderr)
	if err != nil {
		return err
	}
	if resp.PairingResult == nil {
		fmt.Fprintln(stderr, "operator pair: the broker sent no pairing state")
		return errReported
	}
	target, err := pairTargets[req.PairingOp](resp.PairingResult, req.PairingTarget)
	if err != nil {
		fmt.Fprintf(stderr, "operator pair %s %q: %v; nothing sent\n", req.PairingOp, req.PairingTarget, err)
		return errReported
	}
	req.PairingTarget = target
	return nil
}

// pairRead is the read a request needs first, if any; a cancel naming its
// invitation needs none.
func pairRead(req protocol.Message) string {
	if req.PairingOp == protocol.PairingOpCancel && req.PairingTarget != "" {
		return ""
	}
	return pairVerbs[req.PairingOp].read
}

func writePairing(w io.Writer, s *protocol.PairingSnapshot) {
	if s == nil {
		return
	}
	writeSelfIdentity(w, s)
	writeInvitation(w, s.Invitation)
	if len(s.Requests) == 0 && !s.RequestsTruncated {
		fmt.Fprintln(w, "No pairing requests.")
	}
	for _, r := range s.Requests {
		fmt.Fprintf(w, "%q  %q  %s\n", r.IdentityKey, r.Name, protocol.PairingStateLabel(r.State))
	}
	if s.RequestsTruncated {
		fmt.Fprintf(w, "(showing %d of %d requests; pending ones list first, so deciding these brings the rest into view)\n", len(s.Requests), s.RequestsTotal)
	}
	writeJoins(w, s)
}

// writeJoins lists this workspace's redemptions, newest first.
func writeJoins(w io.Writer, s *protocol.PairingSnapshot) {
	if s.JoinsTotal == 0 {
		return
	}
	fmt.Fprintf(w, "Redemptions (%d):\n", s.JoinsTotal)
	for _, j := range s.Joins {
		fmt.Fprintf(w, "%q  %q  %s\n", j.OperationID, j.Destination, j.State)
	}
	if s.JoinsTruncated {
		fmt.Fprintf(w, "(showing the newest %d of %d; status OPERATION_ID shows any one)\n", len(s.Joins), s.JoinsTotal)
	}
}

// writeSelfIdentity prints the fingerprint for the operators to compare by
// phone, or why it is unavailable.
func writeSelfIdentity(w io.Writer, s *protocol.PairingSnapshot) {
	if s.IdentityError != "" {
		fmt.Fprintf(w, "This workspace's identity is unavailable: %q; opening a code needs it\n", s.IdentityError)
		return
	}
	if s.SelfIdentity != "" {
		fmt.Fprintf(w, "This workspace: %q\n", s.SelfIdentity)
	}
}

func writeInvitation(w io.Writer, inv *protocol.InvitationView) {
	if inv == nil {
		fmt.Fprintln(w, "No open invitation.")
		return
	}
	if inv.Code != "" {
		code := inv.Code
		if hasControl(code) {
			code = fmt.Sprintf("%q", code)
		}
		fmt.Fprintf(w, "Code: %s (one-time, shown only now; never send it in chat)\n", code)
		fmt.Fprintln(w, protocol.PairingPreview)
	}
	fmt.Fprintf(w, "Invitation %q: %s\n", inv.ID, inv.Summary())
}
