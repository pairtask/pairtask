package messaging

import (
	"bytes"
	"context"
)

// A v9 message session agrees the pairing before any traffic (#276): right
// after the hellos the dialer sends its PairingEpoch, the other side reads
// it and sends its own, and each side compares. Different pairings, a
// legacy one included, end the session on both sides with ErrOtherPairing.

// EpochStore is a store that names the pairing it holds: nil PairingID for
// a legacy contact, and the lowest version its contact may speak. A store
// without it is legacy, from MinVersion.
type EpochStore interface {
	PairingID() []byte
	MinVersion() byte
}

// VersionRecorder is a store that records the version each session agreed.
type VersionRecorder interface {
	SawVersion(ctx context.Context, v byte) error
}

// sessionPairing is what a session's store says of its pairing, read
// before the store is wrapped.
type sessionPairing struct {
	id       []byte
	min      byte
	recorder VersionRecorder
}

// pairingOf reads a store's pairing: legacy, from MinVersion, when it
// names none.
func pairingOf(st Store) sessionPairing {
	p := sessionPairing{min: MinVersion}
	if e, ok := st.(EpochStore); ok {
		p.id, p.min = e.PairingID(), e.MinVersion()
	}
	p.recorder, _ = st.(VersionRecorder)
	return p
}

// record tells the store the agreed version; its failure is the store's.
func (p sessionPairing) record(ctx context.Context, v byte) error {
	if p.recorder == nil {
		return nil
	}
	return storeErr(p.recorder.SawVersion(ctx, v))
}

// exchangePairing, from PairingVersion on, swaps pairing IDs, dialer
// first, and refuses another pairing.
func (s Session) exchangePairing(dialer bool) error {
	if s.Version < PairingVersion {
		return nil
	}
	theirs, err := s.swapPairing(dialer)
	if err != nil {
		return err
	}
	if !bytes.Equal(theirs, s.pairing.id) {
		return ErrOtherPairing
	}
	return nil
}

// swapPairing sends this side's ID and reads the peer's, in this side's
// order: the dialed side answers with its own even when they differ.
func (s Session) swapPairing(dialer bool) ([]byte, error) {
	if dialer {
		if err := s.sendPairing(); err != nil {
			return nil, err
		}
		return s.receivePairing()
	}
	theirs, err := s.receivePairing()
	if err != nil {
		return nil, err
	}
	return theirs, s.sendPairing()
}

func (s Session) sendPairing() error {
	return s.send(EncodePairingEpoch(PairingEpoch{PairingID: s.pairing.id}))
}

// receivePairing reads the peer's ID: any other frame is a protocol error.
func (s Session) receivePairing() ([]byte, error) {
	frame, err := s.Frames.Recv()
	if err != nil {
		return nil, err
	}
	e, err := DecodePairingEpoch(frame)
	return e.PairingID, err
}
