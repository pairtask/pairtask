package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Known-peer mobility (#284): the host helper's lease and candidates, one
// request each over the operator socket, never a session.

const contactMobilityUsage = "usage: pairtask operator contact discovery | candidates HOST:PORT... [--socket PATH] [--json] [--timeout 5s]"

// contactMobilityOps are the operations this file parses.
var contactMobilityOps = map[string]bool{protocol.ContactOpDiscovery: true, protocol.ContactOpCandidates: true}

// parseContactMobility reads discovery, with no arguments, or candidates,
// with 1 to MaxCandidates endpoints.
func parseContactMobility(args []string, stderr io.Writer) (operatorCall, protocol.Message, int) {
	var call operatorCall
	op := args[0]
	req := protocol.Message{Type: protocol.TypeOperatorContact, MessageID: uuid.NewString(), ContactOp: op}
	flags := flag.NewFlagSet("operator contact "+op, flag.ContinueOnError)
	call.register(flags)
	leading, rest := splitLeading(args[1:])
	code := parseFlags(flags, rest, contactMobilityUsage, stderr, func(more []string) error {
		endpoints := append(leading, more...)
		if err := mobilityArity(op, len(endpoints)); err != nil {
			return err
		}
		if op == protocol.ContactOpCandidates {
			req.ContactCandidates = endpoints
		}
		return call.validate()
	})
	return call, req, code
}

// mobilityArity: discovery takes nothing, candidates 1 to MaxCandidates.
func mobilityArity(op string, n int) error {
	switch {
	case op == protocol.ContactOpDiscovery:
		if n != 0 {
			return fmt.Errorf("contact discovery takes no arguments")
		}
	case !candidateCount(n):
		return fmt.Errorf("contact candidates takes 1 to %d HOST:PORT endpoints", protocol.MaxCandidates)
	}
	return nil
}

// candidateCount: 1 to MaxCandidates.
func candidateCount(n int) bool { return n >= 1 && n <= protocol.MaxCandidates }

// writeMobility prints a lease, or each candidate's outcome.
func writeMobility(w io.Writer, resp protocol.Message) {
	if l := resp.ContactDiscovery; l != nil {
		fmt.Fprintf(w, "advertise %t, browse %t, ports %v, until %s\n", l.Advertise, l.Browse, l.Ports, l.ExpiresAt.Local().Format("15:04:05"))
	}
	for _, r := range resp.ContactCandidatesResult {
		fmt.Fprintf(w, "%q %s", r.Candidate, r.Outcome)
		if r.Contact != "" {
			fmt.Fprintf(w, " %s", r.Contact)
		}
		fmt.Fprintln(w)
	}
}
