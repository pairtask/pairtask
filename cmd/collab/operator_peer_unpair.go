package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"

	"github.com/pairtask/pairtask/internal/peer"
	"github.com/pairtask/pairtask/internal/protocol"
)

// Unpairing a contact (#276) from the CLI: the exact full key, repeated
// with --confirm, and the pairing epoch the operator saw, from --epoch or
// else from the snapshot read just before. A prefix or a name never
// unpairs anything.

// unpairFlags are what only unpair takes.
type unpairFlags struct {
	confirm  string
	epoch    int64
	epochSet bool
}

// register adds --confirm and --epoch to the verb's flags.
func (u *unpairFlags) register(flags *flag.FlagSet) {
	flags.StringVar(&u.confirm, "confirm", "", "unpair: the contact's full identity key again, exactly")
	flags.Func("epoch", "unpair: the pairing epoch shown (default: the current one, read first)", u.setEpoch)
}

// setEpoch takes --epoch: a pairing epoch, 1 or more.
func (u *unpairFlags) setEpoch(value string) error {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return errors.New("--epoch is a pairing epoch: 1 or more")
	}
	u.epoch, u.epochSet = n, true
	return nil
}

// apply checks unpair's arguments and puts them on the request: CONTACT
// is the full key, --confirm repeats it exactly. Other verbs take none.
func (u *unpairFlags) apply(contact string, req *protocol.Message) error {
	if req.ContactOp != protocol.ContactOpUnpair {
		return u.unused()
	}
	if peer.ValidIdentityKey(contact) != nil {
		return errors.New("peer unpair: CONTACT must be the full identity key, never a prefix or a name")
	}
	if u.confirm != contact {
		return errors.New("peer unpair: --confirm must repeat the full identity key exactly")
	}
	req.ContactConfirm, req.ContactEpoch = u.confirm, u.epoch
	return nil
}

// unused refuses unpair's flags on another verb.
func (u *unpairFlags) unused() error {
	if u.confirm != "" || u.epochSet {
		return errors.New("--confirm and --epoch are only for peer unpair")
	}
	return nil
}

// unpairServed: an unpair goes only to a broker that serves it; an older
// one is refused here, before anything is sent.
func unpairServed(req protocol.Message, snapshot *protocol.PairingSnapshot, stderr io.Writer) bool {
	if req.ContactOp != protocol.ContactOpUnpair || snapshot.UnpairAvailable {
		return true
	}
	fmt.Fprintln(stderr, "operator peer unpair: this broker can't unpair a workspace; upgrade it; nothing sent")
	return false
}

// unpairEpoch fills an unpair's epoch, when none was given, from the
// snapshot just read; a contact listed without one is refused.
func unpairEpoch(req *protocol.Message, snapshot *protocol.PairingSnapshot, stderr io.Writer) bool {
	if req.ContactOp != protocol.ContactOpUnpair || req.ContactEpoch > 0 {
		return true
	}
	for _, c := range snapshot.Contacts {
		if c.IdentityKey == req.ContactKey && c.PairingEpoch > 0 {
			req.ContactEpoch = c.PairingEpoch
			return true
		}
	}
	fmt.Fprintln(stderr, "operator peer unpair: the broker listed no pairing epoch for it; pass --epoch; nothing sent")
	return false
}

// writeContactUnpair says what the unpair did, and under which epoch.
func writeContactUnpair(w io.Writer, resp protocol.Message) {
	u := resp.ContactUnpaired
	if u == nil {
		return
	}
	if !u.Changed {
		fmt.Fprintf(w, "Already unpaired %s (epoch %d); nothing changed\n", resp.ContactKey, u.Epoch)
		return
	}
	fmt.Fprintf(w, "Unpaired %s, epoch %d: history kept here; nothing more is sent or taken\n", resp.ContactKey, u.Epoch)
}
