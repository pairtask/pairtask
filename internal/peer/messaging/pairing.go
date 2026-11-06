package messaging

import (
	"encoding/binary"
	"errors"
)

// Pairing epochs and contact notices (#276): the version 9 frames. Sessions
// don't send them yet; MaxVersion stays below PairingVersion until they do.

// PairingVersion is the first version with FramePairingEpoch; non-legacy
// contacts need it.
const PairingVersion byte = 9

const (
	FramePairingEpoch       byte = 27 // after Hello, both ways, dialer first (and first in a control session)
	FrameContactState       byte = 28 // control sessions only
	FrameContactStateStored byte = 29 // control sessions only: the answer to a ContactState
)

// PairingIDBytes is a non-legacy pairing ID's length: SHA-256 of the domain
// and the transcript.
const PairingIDBytes = 32

// Contact states a notice carries.
const (
	ContactStatePaused   byte = 1
	ContactStateResumed  byte = 2
	ContactStateUnpaired byte = 3
)

// PairingEpoch names the pairing this side holds: empty for a legacy
// contact, else exactly PairingIDBytes.
type PairingEpoch struct {
	PairingID []byte
}

// ContactState is one notice: the pairing it is about, the sender's
// generation and the state.
type ContactState struct {
	PairingID []byte
	Gen       uint64
	State     byte
}

// ContactStateStored acknowledges exactly one notice, echoing its pairing
// and generation.
type ContactStateStored struct {
	PairingID []byte
	Gen       uint64
}

// ErrOtherPairing: the other side holds another pairing.
var ErrOtherPairing = errors.New("messaging: the other side holds another pairing")

var (
	pairingEpochShape       = shape{FramePairingEpoch, 1}
	contactStateShape       = shape{FrameContactState, 3}
	contactStateStoredShape = shape{FrameContactStateStored, 2}
)

// validPairingID: empty for a legacy contact, else exactly PairingIDBytes.
func validPairingID(id []byte) bool { return len(id) == 0 || len(id) == PairingIDBytes }

// pairingID is an ID as it arrived: nil when empty.
func pairingID(field string) []byte {
	if field == "" {
		return nil
	}
	return []byte(field)
}

// genField is a generation as it travels: 8 bytes, big-endian.
func genField(gen uint64) string { return string(binary.BigEndian.AppendUint64(nil, gen)) }

// genValue reads a generation field; any other length reads as 0, which
// no notice carries.
func genValue(field string) uint64 {
	if len(field) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64([]byte(field))
}

// stateValue reads a one-byte state field; any other length reads as 0,
// which is no state.
func stateValue(field string) byte {
	if len(field) != 1 {
		return 0
	}
	return field[0]
}

func validContactState(c ContactState) bool {
	known := c.State >= ContactStatePaused && c.State <= ContactStateUnpaired
	return known && validContactStateStored(ContactStateStored{PairingID: c.PairingID, Gen: c.Gen})
}

func validContactStateStored(s ContactStateStored) bool {
	return s.Gen != 0 && validPairingID(s.PairingID)
}

var (
	pairingEpochCodec = fieldCodec[PairingEpoch]{pairingEpochShape, "pairing ID",
		func(e PairingEpoch) []string { return []string{string(e.PairingID)} },
		func(f []string) PairingEpoch { return PairingEpoch{PairingID: pairingID(f[0])} },
		func(e PairingEpoch) bool { return validPairingID(e.PairingID) }}
	contactStateCodec = fieldCodec[ContactState]{contactStateShape, "contact state",
		func(c ContactState) []string {
			return []string{string(c.PairingID), genField(c.Gen), string([]byte{c.State})}
		},
		func(f []string) ContactState {
			return ContactState{PairingID: pairingID(f[0]), Gen: genValue(f[1]), State: stateValue(f[2])}
		},
		validContactState}
	contactStateStoredCodec = fieldCodec[ContactStateStored]{contactStateStoredShape, "contact state answer",
		func(s ContactStateStored) []string { return []string{string(s.PairingID), genField(s.Gen)} },
		func(f []string) ContactStateStored {
			return ContactStateStored{PairingID: pairingID(f[0]), Gen: genValue(f[1])}
		},
		validContactStateStored}
)

func EncodePairingEpoch(e PairingEpoch) ([]byte, error) { return pairingEpochCodec.encode(e) }

func DecodePairingEpoch(frame []byte) (PairingEpoch, error) { return pairingEpochCodec.decode(frame) }

// EncodeContactState refuses Gen 0 and an unknown State.
func EncodeContactState(c ContactState) ([]byte, error) { return contactStateCodec.encode(c) }

func DecodeContactState(frame []byte) (ContactState, error) { return contactStateCodec.decode(frame) }

func EncodeContactStateStored(s ContactStateStored) ([]byte, error) {
	return contactStateStoredCodec.encode(s)
}

func DecodeContactStateStored(frame []byte) (ContactStateStored, error) {
	return contactStateStoredCodec.decode(frame)
}
