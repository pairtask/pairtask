package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/pairtask/pairtask/internal/protocol"
)

// resolveContact turns what was typed into one confirmed contact's full
// key: the exact key, else the one key it prefixes, else the one contact
// with that name. Nothing is sent otherwise. The snapshot it read comes
// with it, for what the broker serves.
func resolveContact(ctx context.Context, call operatorCall, typed string, stderr io.Writer) (string, *protocol.PairingSnapshot, error) {
	resp, err := call.expect(ctx, protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: uuid.NewString(), PairingOp: protocol.PairingOpList},
		expectation{protocol.TypeOperatorPairingResult, "operator pair list"}, stderr)
	if err != nil {
		return "", nil, err
	}
	if resp.PairingResult == nil {
		fmt.Fprintln(stderr, "operator peer: the broker sent no contacts")
		return "", nil, errReported
	}
	key, err := matchContact(resp.PairingResult.Contacts, typed)
	if err != nil {
		fmt.Fprintf(stderr, "operator peer %q: %v; nothing sent\n", typed, err)
		return "", nil, errReported
	}
	return key, resp.PairingResult, nil
}

// contactMatchers are tried in order; the first that names exactly one
// contact wins, and one naming several is ambiguous.
var contactMatchers = []func(c protocol.PairingContactView, typed string) bool{
	func(c protocol.PairingContactView, typed string) bool { return c.IdentityKey == typed },
	func(c protocol.PairingContactView, typed string) bool { return strings.HasPrefix(c.IdentityKey, typed) },
	// A name matches what this side calls a contact and what it calls
	// itself alike: one typed name naming two identities is ambiguous.
	func(c protocol.PairingContactView, typed string) bool { return c.LocalName == typed || c.Name == typed },
}

func matchContact(contacts []protocol.PairingContactView, typed string) (string, error) {
	for _, matches := range contactMatchers {
		var found []protocol.PairingContactView
		for _, c := range contacts {
			if matches(c, typed) {
				found = append(found, c)
			}
		}
		switch {
		case len(found) > 1:
			return "", fmt.Errorf("names %d contacts; type more of the key", len(found))
		case len(found) == 1 && !found[0].Confirmed:
			return "", errors.New("that contact isn't confirmed on both sides yet")
		case len(found) == 1:
			return found[0].IdentityKey, nil
		}
	}
	return "", errors.New("no such contact")
}
