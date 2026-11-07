package peer

import (
	"errors"
	"time"
)

type ExchangeMode string

const (
	ModeLegacy ExchangeMode = "legacy"
	ModeEpoch  ExchangeMode = "epoch"
)

type ExchangeKind string

const (
	KindPair        ExchangeKind = "pair"
	KindRepair      ExchangeKind = "repair"
	KindAddressMove ExchangeKind = "address_move"
)

type ExchangeRecord struct {
	XSeq                                           int64
	OperationID, IdentityKey, Role                 string
	Kind                                           ExchangeKind
	Mode                                           ExchangeMode
	TranscriptID                                   []byte
	BegunMark                                      uint64
	State                                          string
	Decision                                       PairingState
	DecidedAt, DeliveredAt, InstalledAt, RetiredAt time.Time
	InstalledEpoch                                 int64
	Reason                                         string
}
type InstallResult struct {
	Exchange  ExchangeRecord
	Changed   bool
	Epoch     int64
	PairingID []byte
}
type ExchangeDelivery struct {
	IdentityKey, OperationID string
	Decision                 PairingState
	At                       time.Time
}

var (
	ErrCrossedTransition = errors.New("crossed transition")
	ErrAmbiguousPairing  = errors.New("ambiguous pairing")
	ErrJoinRetired       = errors.New("join retired")
	ErrOperationRequired = errors.New("operation required")
)

func PairingID(transcript []byte) []byte { return nil }
