package desktop

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

// The extension's "Workspace address" setting (#388): the person at this
// Desktop types where a paired workspace now is, and the node moves it
// there by the same checked change as pair_address, never by a CLI call.
// HOST:PORT alone is for a node that dials one workspace; NAME=HOST:PORT,
// comma-separated, names each.

// AddressEntry is one workspace the setting moves: Contact is empty for a
// bare HOST:PORT.
type AddressEntry struct {
	Contact     string
	Destination string
}

// AddressOutcome is what the setting did for one entry: moved, unchanged
// (already there, so nothing was probed), checking (another Desktop copy's
// check of the same move is running), or refused with why.
type AddressOutcome struct {
	Contact     string `json:"contact"`
	Destination string `json:"destination"`
	Result      string `json:"result"`
	Reason      string `json:"reason,omitempty"`
}

const (
	addressMoved     = "moved"
	addressUnchanged = "unchanged"
	addressChecking  = "checking"
	addressRefused   = "refused"
)

// checkRunning is the broker's refusal while a check of the workspace's
// address runs, here another copy applying the same setting.
const checkRunning = "already running"

// ParseAddressSetting reads the setting; empty is no entry.
func ParseAddressSetting(raw string) ([]AddressEntry, error) {
	var entries []AddressEntry
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		e, err := addressEntry(part)
		if err != nil {
			return nil, err
		}
		if seen[e.Contact] {
			return nil, errors.New("the setting names " + quoted(e.Contact) + " more than once")
		}
		seen[e.Contact] = true
		entries = append(entries, e)
	}
	if len(entries) > 1 && seen[""] {
		return nil, errors.New("with several entries, name each: NAME=HOST:PORT")
	}
	return entries, nil
}

// addressEntry is one NAME=HOST:PORT or bare HOST:PORT.
func addressEntry(part string) (AddressEntry, error) {
	var e AddressEntry
	if name, dest, named := strings.Cut(part, "="); named {
		e.Contact, part = strings.TrimSpace(name), strings.TrimSpace(dest)
		if e.Contact == "" {
			return e, errors.New("an entry has = but no name before it")
		}
	}
	if peer.CheckDestination(part) != nil {
		return e, errors.New(quoted(part) + " isn't HOST:PORT with a port from 1 to 65535")
	}
	e.Destination = part
	return e, nil
}

// addressSetting holds the outcomes of the setting this copy applied.
type addressSetting struct {
	mu       sync.Mutex
	outcomes []AddressOutcome
}

func (s *addressSetting) set(outcomes []AddressOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes = outcomes
}

func (s *addressSetting) get() []AddressOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]AddressOutcome(nil), s.outcomes...)
}

// ApplyAddressSetting moves each workspace the setting names whose address
// differs, and keeps the outcomes for pair_status. Every Desktop copy
// applies it, through the broker the owning copy runs, so a setting changed
// in one surface counts even while another owns the node; the broker runs
// one check per workspace at a time, and a workspace already there isn't
// probed again.
func (n *Node) ApplyAddressSetting(ctx context.Context, raw string) []AddressOutcome {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	entries, err := ParseAddressSetting(raw)
	if err != nil {
		outcomes := []AddressOutcome{{Result: addressRefused, Reason: "Workspace address setting: " + err.Error()}}
		n.addresses.set(outcomes)
		return outcomes
	}
	outcomes := make([]AddressOutcome, 0, len(entries))
	for _, e := range entries {
		outcomes = append(outcomes, n.applyAddress(ctx, e))
	}
	n.addresses.set(outcomes)
	return outcomes
}

// applyAddress moves one entry's workspace, unless it is there already.
func (n *Node) applyAddress(ctx context.Context, e AddressEntry) AddressOutcome {
	out := AddressOutcome{Contact: e.Contact, Destination: e.Destination}
	c, err := n.settingContact(ctx, e.Contact)
	if err != nil {
		out.Result, out.Reason = addressRefused, err.Error()
		return out
	}
	out.Contact = contactName(c)
	if c.Destination == e.Destination {
		out.Result = addressUnchanged
		return out
	}
	_, err = n.move(ctx, AddressArgs{Contact: c.IdentityKey, Destination: e.Destination})
	out.Result, out.Reason = moveResult(err)
	return out
}

// moveResult is a move's outcome and reason.
func moveResult(err error) (string, string) {
	switch {
	case err == nil:
		return addressMoved, ""
	case strings.Contains(err.Error(), checkRunning):
		return addressChecking, "another Desktop surface is checking this address; pair_status shows where it ends"
	}
	return addressRefused, err.Error()
}

// settingContact is the workspace an entry names, or for a bare HOST:PORT
// the one workspace this node dials.
func (n *Node) settingContact(ctx context.Context, name string) (protocol.PairingContactView, error) {
	if name != "" {
		return n.contact(ctx, name)
	}
	snap, err := n.pairing(ctx)
	if err != nil {
		return protocol.PairingContactView{}, err
	}
	var dialled []protocol.PairingContactView
	for _, c := range snap.Contacts {
		if c.Dials && c.UnpairedAt == nil {
			dialled = append(dialled, c)
		}
	}
	if len(dialled) != 1 {
		return protocol.PairingContactView{}, errors.New("a bare HOST:PORT needs exactly one workspace this node dials; name it: NAME=HOST:PORT")
	}
	return dialled[0], nil
}
