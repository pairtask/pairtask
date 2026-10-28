package peer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
)

// Pairing frames (#178) carry what surrounds the code exchange: the hellos
// that correlate both sides, a refusal and the inviter's receipt. A frame is
// a version byte, a type byte and its fields in a fixed order, each with a
// big-endian uint16 length; nothing may follow. Decoding checks the shape
// only; IDs, names and fingerprints are validated where they are used.

// MaxFrameBytes bounds every pairing frame, including the exchange's own.
const MaxFrameBytes = 4096

const frameVersion byte = 1

// Frame types.
const (
	FrameJoinerHello  byte = 1
	FrameInviterHello byte = 2
	FrameRefusal      byte = 3
	FrameReceipt      byte = 4
	// Settlement, on a contact connection.
	FrameSettle      byte = 5
	FrameSettleReply byte = 6
	FrameAck         byte = 7
	FrameAckReceipt  byte = 8
)

// SettleReceipt is whether the inviter holds the exact outcome of a join.
type SettleReceipt byte

const (
	ReceiptUnknown SettleReceipt = 0
	ReceiptMatched SettleReceipt = 1
)

// ErrPairingProtocol is a malformed, unexpected or oversized frame.
var ErrPairingProtocol = errors.New("peer: pairing protocol error")

// JoinerHello opens a join: the joiner's own operation ID and display name.
type JoinerHello struct {
	OperationID, Name string
}

// InviterHello answers with the invitation and the inviter's operation ID.
type InviterHello struct {
	InvitationID, OperationID string
}

// Refusal is why the inviter won't start an attempt.
type Refusal struct {
	Reason string
}

// FrameKind is a frame's type, after checking its version and size.
func FrameKind(frame []byte) (byte, error) {
	if len(frame) < 2 || len(frame) > MaxFrameBytes {
		return 0, fmt.Errorf("%w: frame size", ErrPairingProtocol)
	}
	if frame[0] != frameVersion || !knownFrame(frame[1]) {
		return 0, fmt.Errorf("%w: unknown frame version or type", ErrPairingProtocol)
	}
	return frame[1], nil
}

func knownFrame(kind byte) bool {
	return kind >= FrameJoinerHello && kind <= FrameAckReceipt
}

func EncodeJoinerHello(h JoinerHello) ([]byte, error) {
	return encodeFrame(FrameJoinerHello, []byte(h.OperationID), []byte(h.Name))
}

func DecodeJoinerHello(frame []byte) (JoinerHello, error) {
	f, err := decodeFrame(frame, FrameJoinerHello, 2)
	if err != nil {
		return JoinerHello{}, err
	}
	return JoinerHello{OperationID: string(f[0]), Name: string(f[1])}, nil
}

func EncodeInviterHello(h InviterHello) ([]byte, error) {
	return encodeFrame(FrameInviterHello, []byte(h.InvitationID), []byte(h.OperationID))
}

func DecodeInviterHello(frame []byte) (InviterHello, error) {
	f, err := decodeFrame(frame, FrameInviterHello, 2)
	if err != nil {
		return InviterHello{}, err
	}
	return InviterHello{InvitationID: string(f[0]), OperationID: string(f[1])}, nil
}

func EncodeRefusal(r Refusal) ([]byte, error) {
	return encodeFrame(FrameRefusal, []byte(r.Reason))
}

func DecodeRefusal(frame []byte) (Refusal, error) {
	f, err := decodeFrame(frame, FrameRefusal, 1)
	if err != nil {
		return Refusal{}, err
	}
	return Refusal{Reason: string(f[0])}, nil
}

func EncodeReceipt(r JoinReceipt) ([]byte, error) {
	return encodeFrame(FrameReceipt, []byte(r.InvitationID), []byte(r.InviterOperationID), []byte(r.Joiner), r.TranscriptID)
}

func DecodeReceipt(frame []byte) (JoinReceipt, error) {
	f, err := decodeFrame(frame, FrameReceipt, 4)
	if err != nil {
		return JoinReceipt{}, err
	}
	return JoinReceipt{InvitationID: string(f[0]), InviterOperationID: string(f[1]), Joiner: string(f[2]), TranscriptID: f[3]}, nil
}

func encodeFrame(kind byte, fields ...[]byte) ([]byte, error) {
	frame := []byte{frameVersion, kind}
	for _, field := range fields {
		if len(field) > math.MaxUint16 {
			return nil, fmt.Errorf("%w: field too long", ErrPairingProtocol)
		}
		frame = binary.BigEndian.AppendUint16(frame, uint16(len(field)))
		frame = append(frame, field...)
	}
	if len(frame) > MaxFrameBytes {
		return nil, fmt.Errorf("%w: frame too long", ErrPairingProtocol)
	}
	return frame, nil
}

// decodeFrame splits a frame of the wanted type into exactly n fields, each
// a copy.
func decodeFrame(frame []byte, kind byte, n int) ([][]byte, error) {
	got, err := FrameKind(frame)
	if err != nil {
		return nil, err
	}
	if got != kind {
		return nil, fmt.Errorf("%w: unexpected frame type %d", ErrPairingProtocol, got)
	}
	rest := frame[2:]
	fields := make([][]byte, 0, n)
	for range n {
		var field []byte
		field, rest, err = nextField(rest)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("%w: trailing bytes", ErrPairingProtocol)
	}
	return fields, nil
}

func nextField(b []byte) (field, rest []byte, err error) {
	if len(b) < 2 {
		return nil, nil, fmt.Errorf("%w: truncated frame", ErrPairingProtocol)
	}
	size := int(binary.BigEndian.Uint16(b))
	if len(b)-2 < size {
		return nil, nil, fmt.Errorf("%w: truncated field", ErrPairingProtocol)
	}
	return append([]byte(nil), b[2:2+size]...), b[2+size:], nil
}

// Settle asks the inviter, over a contact connection, what became of one
// join.
type Settle struct {
	JoinerOperationID, InviterOperationID, InvitationID string
	TranscriptID                                        []byte
}

// SettleReply is the inviter's durable answer: whether it holds the exact
// outcome, and the request's decision.
type SettleReply struct {
	Receipt  SettleReceipt
	Decision PairingState
}

// Ack acknowledges a decision; AckReceipt confirms the ack was recorded.
type Ack struct {
	JoinerOperationID string
	Decision          PairingState
}

type AckReceipt Ack

func EncodeSettle(m Settle) ([]byte, error) {
	return encodeFrame(FrameSettle, []byte(m.JoinerOperationID), []byte(m.InviterOperationID), []byte(m.InvitationID), m.TranscriptID)
}

func DecodeSettle(frame []byte) (Settle, error) {
	f, err := decodeFrame(frame, FrameSettle, 4)
	if err != nil {
		return Settle{}, err
	}
	return Settle{JoinerOperationID: string(f[0]), InviterOperationID: string(f[1]), InvitationID: string(f[2]), TranscriptID: f[3]}, nil
}

var replyDecisions = []PairingState{PairingPending, PairingAccepted, PairingRejected}

func EncodeSettleReply(m SettleReply) ([]byte, error) {
	return encodeFrame(FrameSettleReply, []byte{byte(m.Receipt)}, []byte(m.Decision))
}

func DecodeSettleReply(frame []byte) (SettleReply, error) {
	f, err := decodeFrame(frame, FrameSettleReply, 2)
	if err != nil {
		return SettleReply{}, err
	}
	receipt, err := parseReceipt(f[0])
	if err != nil {
		return SettleReply{}, err
	}
	decision := PairingState(f[1])
	if !slices.Contains(replyDecisions, decision) {
		return SettleReply{}, fmt.Errorf("%w: bad settle decision", ErrPairingProtocol)
	}
	return SettleReply{Receipt: receipt, Decision: decision}, nil
}

// parseReceipt accepts exactly one byte naming a known receipt.
func parseReceipt(field []byte) (SettleReceipt, error) {
	if len(field) != 1 || SettleReceipt(field[0]) > ReceiptMatched {
		return 0, fmt.Errorf("%w: bad settle receipt", ErrPairingProtocol)
	}
	return SettleReceipt(field[0]), nil
}

func EncodeAck(m Ack) ([]byte, error) {
	return encodeFrame(FrameAck, []byte(m.JoinerOperationID), []byte(m.Decision))
}

func DecodeAck(frame []byte) (Ack, error) {
	return decodeAckFrame(frame, FrameAck)
}

func EncodeAckReceipt(m AckReceipt) ([]byte, error) {
	return encodeFrame(FrameAckReceipt, []byte(m.JoinerOperationID), []byte(m.Decision))
}

func DecodeAckReceipt(frame []byte) (AckReceipt, error) {
	ack, err := decodeAckFrame(frame, FrameAckReceipt)
	return AckReceipt(ack), err
}

// decodeAckFrame accepts only a final decision.
func decodeAckFrame(frame []byte, kind byte) (Ack, error) {
	f, err := decodeFrame(frame, kind, 2)
	if err != nil {
		return Ack{}, err
	}
	ack := Ack{JoinerOperationID: string(f[0]), Decision: PairingState(f[1])}
	if _, final := decisionStates[ack.Decision]; !final {
		return Ack{}, fmt.Errorf("%w: ack of a non-final decision", ErrPairingProtocol)
	}
	return ack, nil
}
