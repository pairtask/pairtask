package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Known-peer mobility (#284): the host helper's lease and candidates, one
// request each over the operator socket, never a session.

const contactMobilityUsage = "usage: pairtask operator contact discovery [--report JSON] | candidates HOST:PORT... | find [--socket PATH] [--json] [--timeout 5s]"

// contactMobilityOps are the operations this file parses.
var contactMobilityOps = map[string]bool{protocol.ContactOpDiscovery: true, protocol.ContactOpCandidates: true, protocol.ContactOpFind: true}

// discoveryReport is the helper's report on discovery (#284): what it heard
// and whether it announced, as the request fields themselves, validated by
// the broker like any frame.
type discoveryReport struct {
	Found      []protocol.FoundInvitation `json:"contact_found"`
	Devices    []protocol.TailnetDevice   `json:"contact_devices"`
	Announcing *protocol.Announcing       `json:"contact_announcing"`
}

// parseContactMobility reads discovery, with no arguments, or candidates,
// with 1 to MaxCandidates endpoints.
func parseContactMobility(args []string, stderr io.Writer) (operatorCall, protocol.Message, int) {
	var call operatorCall
	op := args[0]
	req := protocol.Message{Type: protocol.TypeOperatorContact, MessageID: uuid.NewString(), ContactOp: op}
	flags := flag.NewFlagSet("operator contact "+op, flag.ContinueOnError)
	call.register(flags)
	report := flags.String("report", "", "discovery: the helper's report as JSON (contact_found, contact_devices, contact_announcing)")
	leading, rest := splitLeading(args[1:])
	code := parseFlags(flags, rest, contactMobilityUsage, stderr, func(more []string) error {
		endpoints := append(leading, more...)
		if err := mobilityArity(op, len(endpoints)); err != nil {
			return err
		}
		if op == protocol.ContactOpCandidates {
			req.ContactCandidates = endpoints
		}
		if err := applyReport(&req, op, *report); err != nil {
			return err
		}
		return call.validate()
	})
	return call, req, code
}

// applyReport puts a discovery report's fields on the request; it belongs
// to discovery only and must be an object of at most 64 KiB.
func applyReport(req *protocol.Message, op, raw string) error {
	if raw == "" {
		return nil
	}
	if op != protocol.ContactOpDiscovery {
		return fmt.Errorf("--report belongs to contact discovery only")
	}
	if len(raw) > 64<<10 {
		return fmt.Errorf("--report is over 64 KiB")
	}
	var r discoveryReport
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return fmt.Errorf("--report: %w", err)
	}
	req.ContactFound, req.ContactDevices, req.ContactAnnouncing = r.Found, r.Devices, r.Announcing
	return nil
}

// mobilityArity: discovery and find take nothing, candidates 1 to MaxCandidates.
func mobilityArity(op string, n int) error {
	switch {
	case op == protocol.ContactOpDiscovery || op == protocol.ContactOpFind:
		if n != 0 {
			return fmt.Errorf("contact %s takes no arguments", op)
		}
	case !candidateCount(n):
		return fmt.Errorf("contact candidates takes 1 to %d HOST:PORT endpoints", protocol.MaxCandidates)
	}
	return nil
}

// writeLease prints a lease on one line.
func writeLease(w io.Writer, l protocol.ContactDiscovery) {
	fmt.Fprintf(w, "advertise %t, browse %t, ports %v, until %s", l.Advertise, l.Browse, l.Ports, l.ExpiresAt.Local().Format("15:04:05"))
	if l.Invitation != nil {
		fmt.Fprintf(w, ", announce %q", l.Invitation.Name)
	}
	fmt.Fprintf(w, ", find %t\n", l.Find)
}

// writeFind prints find's snapshot: when the helper last reported, the
// invitations heard and the tailnet devices.
func writeFind(w io.Writer, f protocol.ContactFind) {
	if f.HelperSeenAt == nil {
		fmt.Fprintln(w, "helper: not seen since this broker started")
	} else {
		fmt.Fprintf(w, "helper: seen %s\n", f.HelperSeenAt.Local().Format("15:04:05"))
	}
	for _, inv := range f.Invitations {
		fmt.Fprintf(w, "invitation %q at %s:%d, seen %s\n", inv.Name, inv.Host, inv.Port, inv.SeenAt.Local().Format("15:04:05"))
	}
	for _, d := range f.Devices {
		fmt.Fprintf(w, "device %q %s\n", d.Name, d.Address)
	}
}

// candidateCount: 1 to MaxCandidates.
func candidateCount(n int) bool { return n >= 1 && n <= protocol.MaxCandidates }

// writeMobility prints a lease, or each candidate's outcome.
func writeMobility(w io.Writer, resp protocol.Message) {
	if l := resp.ContactDiscovery; l != nil {
		writeLease(w, *l)
	}
	if f := resp.ContactFind; f != nil {
		writeFind(w, *f)
	}
	for _, r := range resp.ContactCandidatesResult {
		fmt.Fprintf(w, "%q %s", r.Candidate, r.Outcome)
		if r.Contact != "" {
			fmt.Fprintf(w, " %s", r.Contact)
		}
		fmt.Fprintln(w)
	}
}
