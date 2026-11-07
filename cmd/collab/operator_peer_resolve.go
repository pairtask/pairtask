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
func resolveContact(ctx context.Context, call operatorCall, q contactQuery, stderr io.Writer) (string, *protocol.PairingSnapshot, error) {
	resp, err := call.expect(ctx, protocol.Message{Type: protocol.TypeOperatorPairing, MessageID: uuid.NewString(), PairingOp: protocol.PairingOpList},
		expectation{protocol.TypeOperatorPairingResult, "operator pair list"}, stderr)
	if err != nil {
		return "", nil, err
	}
	if resp.PairingResult == nil {
		fmt.Fprintln(stderr, "operator peer: the broker sent no contacts")
		return "", nil, errReported
	}
	key, err := q.match(resp.PairingResult.Contacts)
	if err != nil {
		fmt.Fprintf(stderr, "operator peer %q: %v; nothing sent\n", q.typed, err)
		return "", nil, errReported
	}
	return key, resp.PairingResult, nil
}

// contactQuery is what was typed for CONTACT, and whether the operation
// also reaches a contact this side unpaired (#276): its history is listed
// and an unpair retried, by the exact key only. The broker refuses
// anything else such a contact is asked.
type contactQuery struct {
	typed    string
	retained bool
}

// retainedOps are the contact operations an unpaired contact still takes.
var retainedOps = map[string]bool{protocol.ContactOpList: true, protocol.ContactOpUnpair: true}

func queryFor(typed, op string) contactQuery {
	return contactQuery{typed: typed, retained: retainedOps[op]}
}

// contactMatchers are tried in order; the first that names exactly one
// contact wins, and one naming several is ambiguous.
var contactMatchers = []func(c protocol.PairingContactView, typed string) bool{
	exactKey,
	func(c protocol.PairingContactView, typed string) bool { return strings.HasPrefix(c.IdentityKey, typed) },
	// A name matches what this side calls a contact and what it calls
	// itself alike: one typed name naming two identities is ambiguous.
	func(c protocol.PairingContactView, typed string) bool { return c.LocalName == typed || c.Name == typed },
}

func exactKey(c protocol.PairingContactView, typed string) bool { return c.IdentityKey == typed }

func matchContact(contacts []protocol.PairingContactView, typed string) (string, error) {
	return contactQuery{typed: typed}.match(contacts)
}

func (q contactQuery) match(contacts []protocol.PairingContactView) (string, error) {
	for _, matches := range contactMatchers {
		var found []protocol.PairingContactView
		for _, c := range contacts {
			if matches(c, q.typed) {
				found = append(found, c)
			}
		}
		switch {
		case len(found) > 1:
			return "", fmt.Errorf("names %d contacts; type more of the key", len(found))
		case len(found) == 1:
			return q.admit(found[0])
		}
	}
	return "", errors.New("no such contact")
}

// admit takes a confirmed contact, or an unpaired one named by its exact
// key for an operation it still takes.
func (q contactQuery) admit(c protocol.PairingContactView) (string, error) {
	if c.Confirmed || q.retainedKey(c) {
		return c.IdentityKey, nil
	}
	return "", errors.New("that contact isn't confirmed on both sides yet")
}

// retainedKey: an unpaired contact, named by its exact key, for an
// operation it still takes.
func (q contactQuery) retainedKey(c protocol.PairingContactView) bool {
	if !q.retained || c.UnpairedAt == nil {
		return false
	}
	return exactKey(c, q.typed)
}
