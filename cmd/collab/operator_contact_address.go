package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

// operator contact address NAME HOST:PORT (#389): /pair address without the
// chat. It makes one request on the operator socket, so it works while the
// operator's chat holds the inbox. The broker dials the new address and
// keeps it only if the contact's own identity answers there; a refusal
// leaves the old one.

const contactAddressUsage = "usage: pairtask operator contact address CONTACT HOST:PORT [--socket PATH] [--json] [--timeout 30s]"

// addressTimeout covers the broker's probe of the new address, which can
// take up to its whole attempt budget.
const addressTimeout = "30s"

// runContactAddress moves the contact args name to a new address.
func runContactAddress(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, typed, destination, code := parseContactAddress(args, stderr)
	if code != -1 {
		return code
	}
	key, snapshot, err := resolveContact(ctx, call, queryFor(typed, protocol.ContactOpAddress), stderr)
	if err != nil {
		return 1
	}
	if !dialsContact(snapshot, key) {
		fmt.Fprintf(stderr, "operator contact address: this side doesn't dial %q; they update your address on their side; nothing sent\n", typed)
		return 1
	}
	req := protocol.Message{Type: protocol.TypeOperatorContact, MessageID: uuid.NewString(), ContactOp: protocol.ContactOpAddress,
		ContactKey: key, ContactText: destination}
	resp, err := call.expect(ctx, req, expectation{protocol.TypeOperatorContactResult, "operator contact address"}, stderr)
	if err != nil {
		return 1
	}
	if call.asJSON {
		return encodeJSON(stdout, stderr, resp)
	}
	return writeMoved(stdout, stderr, resp.PairingResult, key)
}

// parseContactAddress reads CONTACT, HOST:PORT and the call's flags; the
// timeout defaults to the probe's budget.
func parseContactAddress(args []string, stderr io.Writer) (operatorCall, string, string, int) {
	var call operatorCall
	var typed, destination string
	flags := flag.NewFlagSet("operator contact address", flag.ContinueOnError)
	call.register(flags)
	_ = flags.Set("timeout", addressTimeout) // a typed --timeout still wins
	leading, rest := splitLeading(args)
	code := parseFlags(flags, rest, contactAddressUsage, stderr, func(more []string) error {
		positional := append(leading, more...)
		if len(positional) != 2 {
			return errors.New("contact address: give CONTACT and HOST:PORT")
		}
		typed, destination = positional[0], positional[1]
		if peer.CheckDestination(destination) != nil {
			return errors.New("contact address: HOST:PORT with a port from 1 to 65535")
		}
		return call.validate()
	})
	return call, typed, destination, code
}

// dialsContact: the snapshot lists key, and this side dials it.
func dialsContact(snapshot *protocol.PairingSnapshot, key string) bool {
	for _, c := range snapshot.Contacts {
		if c.IdentityKey == key {
			return c.Dials
		}
	}
	return false
}

// writeMoved says where the contact is now, from the broker's answer.
func writeMoved(stdout, stderr io.Writer, snapshot *protocol.PairingSnapshot, key string) int {
	if snapshot != nil {
		for _, c := range snapshot.Contacts {
			if c.IdentityKey == key {
				fmt.Fprintf(stdout, "%q now at %s (its identity answered there)\n", contactLabel(c), c.Destination)
				return 0
			}
		}
	}
	fmt.Fprintln(stderr, "operator contact address: the broker answered without the contact")
	return 1
}

// contactLabel is what this side calls c, else its own name.
func contactLabel(c protocol.PairingContactView) string {
	if c.LocalName != "" {
		return c.LocalName
	}
	return c.Name
}
