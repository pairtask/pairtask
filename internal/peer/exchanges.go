package peer

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
)

// Pairing exchanges (#276). Every exchange on an epoch-capable connection
// is recorded in an exchange ledger as it commits, under its operation ID.
// The ledger, not the request or join rows, is the authority record of
// such a pairing: what it is (a first pairing, a declared repair or an
// address move), the global authority mark it began under, its decision,
// and, once installed, the pairing epoch it installed. An installed row is
// immutable but for its retirement.

// ExchangeMode is what an exchange can install: an epoch-capable exchange
// installs a pairing epoch with its own pairing ID; a legacy one keeps
// today's decision path. The empty mode reads as legacy.
type ExchangeMode string

const (
	ModeLegacy ExchangeMode = "legacy"
	ModeEpoch  ExchangeMode = "epoch"
)

// Epoch reports an epoch-capable exchange.
func (m ExchangeMode) Epoch() bool { return m == ModeEpoch }

// Stored is the mode as the ledger keeps it: "" is legacy.
func (m ExchangeMode) Stored() ExchangeMode {
	if m.Epoch() {
		return ModeEpoch
	}
	return ModeLegacy
}

// ExchangeKind is an exchange's intent, decided at commit from this side's
// contact for the authenticated identity: a first or fresh pairing, a
// repair the operator declared, or an undeclared exchange with an active
// contact, which only moves its address and never installs.
type ExchangeKind string

const (
	KindPair        ExchangeKind = "pair"
	KindRepair      ExchangeKind = "repair"
	KindAddressMove ExchangeKind = "address_move"
)

// Ledger states and the reasons an exchange is refused.
const (
	ExchangeOpen      = "open"
	ExchangeInstalled = "installed"
	ExchangeRetired   = "retired"
	ExchangeRefused   = "refused"

	RefusedCrossed     = "crossed_transition"
	RefusedAmbiguous   = "ambiguous"
	RefusedAddressMove = "address_move"
	RefusedMismatch    = "identity_mismatch"
)

// ExchangeRecord is one exchange's ledger row.
type ExchangeRecord struct {
	XSeq                     int64
	OperationID, IdentityKey string
	Role                     string // "inviter" | "joiner"
	Kind                     ExchangeKind
	Repair                   string // the identity the exchange declared it repairs, "" for none
	Mode                     ExchangeMode
	TranscriptID             []byte
	BegunMark                uint64 // the global authority mark when the exchange began
	State                    string // ExchangeOpen, ExchangeInstalled, ExchangeRetired or ExchangeRefused
	Decision                 PairingState
	DecidedAt, DeliveredAt   time.Time
	InstalledAt, RetiredAt   time.Time
	InstalledEpoch           int64 // 0 unless it was installed
	Reason                   string
}

// InstallResult is what delivering an exchange did: its ledger row, whether
// this call installed it (false for a replay or an exchange that installs
// nothing), and the pairing epoch and ID it answers: the installed ones, a
// retired row's historical ones, or, for an address move, the contact's
// current ones.
type InstallResult struct {
	Exchange  ExchangeRecord
	Changed   bool
	Epoch     int64
	PairingID []byte
}

// ExchangeDelivery is the joiner's acknowledgement of one exchange's
// decision, pinned to that exchange's operation ID.
type ExchangeDelivery struct {
	IdentityKey, OperationID string
	Decision                 PairingState
	At                       time.Time
}

var (
	// ErrCrossedTransition: an unpair, install, repair or remote unpaired
	// happened while the exchange ran; nothing but its refusal is kept.
	ErrCrossedTransition = errors.New("peer: the pairing changed while this exchange ran; use a fresh code")
	// ErrAmbiguousPairing: two pairings of one identity crossed, and both
	// were refused.
	ErrAmbiguousPairing = errors.New("peer: two pairings crossed; start one fresh repair")
	// ErrJoinRetired: an unpair retired the join; it takes no decision.
	ErrJoinRetired = errors.New("peer: join retired by an unpair")
	// ErrOperationRequired: the identity's candidate is epoch-capable, so its
	// delivery must name its operation.
	ErrOperationRequired = errors.New("peer: the pairing's delivery must name its operation")
	// ErrRepairMismatch: a declared repair authenticated another identity.
	ErrRepairMismatch = fmt.Errorf("peer: repair authenticated another identity: %w", ErrIdentityMismatch)
)

// refusals are the errors a refused exchange answers with; an address move
// is settled, not an error.
var refusals = map[string]error{
	RefusedCrossed:   ErrCrossedTransition,
	RefusedAmbiguous: ErrAmbiguousPairing,
	RefusedMismatch:  ErrRepairMismatch,
}

// RefusalError is a refused exchange's error by its reason; nil for an
// address move.
func RefusalError(reason string) error {
	if reason == RefusedAddressMove {
		return nil
	}
	if err, ok := refusals[reason]; ok {
		return err
	}
	return fmt.Errorf("peer: exchange refused (%s)", reason)
}

// pairingIDDomain separates pairing IDs from any other hash of a
// transcript.
const pairingIDDomain = "pairtask/pairing-epoch/1"

// PairingID is the ID of the pairing an exchange installs:
// SHA-256(domain || transcript), the same on both sides.
func PairingID(transcript []byte) []byte {
	h := sha256.New()
	h.Write([]byte(pairingIDDomain))
	h.Write(transcript)
	return h.Sum(nil)
}
