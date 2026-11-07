package protocol

import (
	"errors"
	"time"
)

// Unpairing a contact (#276): the operator ends the contact's current
// pairing for good, keeping its history readable here. The request binds
// the exact full key twice and the pairing epoch the operator was shown,
// so a stale screen can't end a newer pairing.
const (
	// ContactOpUnpair ends contact_key's pairing under message_id, when
	// contact_confirm repeats contact_key exactly and contact_epoch is the
	// current pairing epoch (1 or more). Unpairing an unpaired contact
	// changes nothing; a retry answers the first result.
	ContactOpUnpair = "unpair"
	// CapabilityContactUnpair: the broker serves unpair.
	CapabilityContactUnpair = "contact_unpair"
	// CapabilityPairingRepair: the broker takes pairing_repair on open and
	// redeem.
	CapabilityPairingRepair = "pairing_repair"

	// ErrContactUnpaired refuses a change to a contact this side unpaired:
	// list and read its history, or pair again.
	ErrContactUnpaired = "contact_unpaired"
	// ErrPairingCrossed: both sides changed the pairing at once; the
	// later change must be made again.
	ErrPairingCrossed = "pairing_crossed"
	// ErrPairingAmbiguousCode: more than one pairing could be meant; name the
	// full key.
	ErrPairingAmbiguousCode = "pairing_ambiguous"
)

// ContactUnpairView is an unpair's durable result: whether it changed the
// contact, the generation after it, the pairing epoch it ended and when.
// A retry answers the same.
type ContactUnpairView struct {
	Changed    bool      `json:"changed"`
	Generation int64     `json:"generation"`
	Epoch      int64     `json:"epoch"`
	UnpairedAt time.Time `json:"unpaired_at"`
}

// validContactUnpair requires an unpair's exact confirmation and the
// positive pairing epoch it was shown.
func validContactUnpair(msg Message, allowed contactFieldSet) error {
	if !allowed.confirm {
		return nil
	}
	if msg.ContactConfirm != msg.ContactKey {
		return errors.New("contact_confirm must repeat contact_key exactly")
	}
	if msg.ContactEpoch <= 0 {
		return errors.New("contact_epoch is the pairing epoch shown: 1 or more")
	}
	return nil
}
