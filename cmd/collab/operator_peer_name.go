package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/pairtask/pairtask/internal/protocol"
)

// nameArguments: name takes a NAME or --clear, never both; --clear is
// name's alone.
func nameArguments(verb string, clear bool, text string) error {
	switch {
	case verb != "name" && clear:
		return errors.New("--clear is only for peer name")
	case verb == "name" && clear == (strings.TrimSpace(text) != ""):
		return errors.New("peer name: give NAME or --clear")
	}
	return nil
}

// writeContactName says what this side now calls the contact; its names
// are quoted, as text that came from people.
func writeContactName(w io.Writer, key string, contacts []protocol.PairingContactView) {
	for _, c := range contacts {
		if c.IdentityKey != key {
			continue
		}
		if c.LocalName == "" {
			fmt.Fprintf(w, "Name cleared for %s; it calls itself %q\n", key, c.Name)
			return
		}
		fmt.Fprintf(w, "This side calls %s %q\n", key, c.LocalName)
		return
	}
}
