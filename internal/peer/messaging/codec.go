package messaging

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

// A frame is a version byte, a type byte and its fields in a fixed order,
// each with a big-endian uint16 length; nothing may follow. Decoding checks
// the shape only; IDs and text are judged by the receiving store, which
// answers Refused rather than dropping the session.

// MaxFrameBytes bounds every message frame.
const MaxFrameBytes = 32 << 10

// Protocol versions this side speaks, as Hello carries them. Version 2
// keeps a session open after its first round (live.go).
const (
	MinVersion byte = 1
	MaxVersion byte = 2
)

// LiveVersion is the first version whose sessions can stay open.
const LiveVersion byte = 2

const frameVersion byte = 1

// Frame types.
const (
	FrameHello       byte = 1
	FrameMessage     byte = 2
	FrameStored      byte = 3
	FrameRefused     byte = 4
	FrameEnd         byte = 5
	FrameRead        byte = 6
	FrameReadStored  byte = 7
	FrameUnsupported byte = 8
	// Version 2 only, never sent to a version 1 peer.
	FrameLive     byte = 9  // after the first round: whether this side stays connected
	FrameRound    byte = 10 // the dialer starts a round
	FrameRoundAck byte = 11 // the dialed side is ready for it
	FramePing     byte = 12 // the dialer, while idle
	FramePong     byte = 13 // the answer to a Ping
	FramePending  byte = 14 // the dialed side has something for the dialer
)

// ErrProtocol is a malformed, unexpected or oversized frame.
var ErrProtocol = errors.New("messaging: protocol error")

// Hello opens a session and answers it: the versions a side speaks.
type Hello struct{ Min, Max byte }

// Message is one message as it travels. ID, N, CreatedAt and Text never
// change once queued; Hash covers all four.
type Message struct {
	ID        string
	N         uint64 // the sender's sequence for this contact, from 1
	CreatedAt time.Time
	Text      string
}

// Hash is the message's identity: a retry must carry the same one.
func (m Message) Hash() [32]byte {
	h := sha256.New()
	for _, part := range []string{m.ID, strconv.FormatUint(m.N, 10), stamp(m.CreatedAt), m.Text} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		h.Write(size[:])
		h.Write([]byte(part))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Stored answers a Message the receiver now holds durably.
type Stored struct{ ID string }

// Refused answers a Message the receiver did not store.
type Refused struct {
	ID string
	Refusal
}

// Read says the sender's message was shown to the reader's operator.
type Read struct{ ID string }

// ReadStored answers a Read, whatever the receiver made of it.
type ReadStored struct{ ID string }

// Unsupported ends a session with no common version.
type Unsupported struct{ Min, Max byte }

// Kind is a frame's type, after checking its version and size.
func Kind(frame []byte) (byte, error) {
	f := wireFrame(frame)
	if !f.sized() {
		return 0, fmt.Errorf("%w: frame size", ErrProtocol)
	}
	if !f.known() {
		return 0, fmt.Errorf("%w: unknown frame version or type", ErrProtocol)
	}
	return frame[1], nil
}

// wireFrame is one frame as it arrived.
type wireFrame []byte

func (f wireFrame) sized() bool { return len(f) >= 2 && len(f) <= MaxFrameBytes }

func (f wireFrame) known() bool {
	return f[0] == frameVersion && f[1] >= FrameHello && f[1] <= FramePending
}

// shape is a frame type and its exact number of fields.
type shape struct {
	kind   byte
	fields int
}

var (
	helloShape       = shape{FrameHello, 2}
	unsupportedShape = shape{FrameUnsupported, 2}
	messageShape     = shape{FrameMessage, 4}
	storedShape      = shape{FrameStored, 1}
	refusedShape     = shape{FrameRefused, 3}
	endShape         = shape{FrameEnd, 0}
	readShape        = shape{FrameRead, 1}
	readStoredShape  = shape{FrameReadStored, 1}
)

func EncodeHello(h Hello) ([]byte, error) { return encode(FrameHello, []byte{h.Min}, []byte{h.Max}) }

func DecodeHello(frame []byte) (Hello, error) {
	min, max, err := decodeVersions(frame, helloShape)
	return Hello{Min: min, Max: max}, err
}

func EncodeUnsupported(u Unsupported) ([]byte, error) {
	return encode(FrameUnsupported, []byte{u.Min}, []byte{u.Max})
}

func DecodeUnsupported(frame []byte) (Unsupported, error) {
	min, max, err := decodeVersions(frame, unsupportedShape)
	return Unsupported{Min: min, Max: max}, err
}

func decodeVersions(frame []byte, want shape) (byte, byte, error) {
	f, err := decode(frame, want)
	if err != nil {
		return 0, 0, err
	}
	if !versionRange(f) {
		return 0, 0, fmt.Errorf("%w: versions", ErrProtocol)
	}
	return f[0][0], f[1][0], nil
}

// versionRange: two fields of one byte each, the lowest not above the
// highest.
func versionRange(f [][]byte) bool {
	if len(f[0]) != 1 || len(f[1]) != 1 {
		return false
	}
	return f[0][0] <= f[1][0]
}

func EncodeMessage(m Message) ([]byte, error) {
	n := binary.BigEndian.AppendUint64(nil, m.N)
	return encode(FrameMessage, []byte(m.ID), n, []byte(stamp(m.CreatedAt)), []byte(m.Text))
}

func DecodeMessage(frame []byte) (Message, error) {
	f, err := decode(frame, messageShape)
	if err != nil {
		return Message{}, err
	}
	if len(f[1]) != 8 {
		return Message{}, fmt.Errorf("%w: sequence", ErrProtocol)
	}
	at, err := time.Parse(time.RFC3339Nano, string(f[2]))
	if err != nil {
		return Message{}, fmt.Errorf("%w: created_at", ErrProtocol)
	}
	return Message{ID: string(f[0]), N: binary.BigEndian.Uint64(f[1]), CreatedAt: at.UTC(), Text: string(f[3])}, nil
}

func EncodeStored(s Stored) ([]byte, error) { return encode(FrameStored, []byte(s.ID)) }

func DecodeStored(frame []byte) (Stored, error) {
	id, err := decodeID(frame, storedShape)
	return Stored{ID: id}, err
}

func EncodeRefused(r Refused) ([]byte, error) {
	return encode(FrameRefused, []byte(r.ID), []byte(r.Class), []byte(r.Reason))
}

func DecodeRefused(frame []byte) (Refused, error) {
	f, err := decode(frame, refusedShape)
	if err != nil {
		return Refused{}, err
	}
	r := Refused{ID: string(f[0]), Refusal: Refusal{Class: string(f[1]), Reason: string(f[2])}}
	if !r.Valid() {
		return Refused{}, fmt.Errorf("%w: refusal class or reason", ErrProtocol)
	}
	return r, nil
}

func EncodeEnd() ([]byte, error) { return encode(FrameEnd) }

func DecodeEnd(frame []byte) error {
	_, err := decode(frame, endShape)
	return err
}

func EncodeRead(r Read) ([]byte, error) { return encode(FrameRead, []byte(r.ID)) }

func DecodeRead(frame []byte) (Read, error) {
	id, err := decodeID(frame, readShape)
	return Read{ID: id}, err
}

func EncodeReadStored(r ReadStored) ([]byte, error) { return encode(FrameReadStored, []byte(r.ID)) }

func DecodeReadStored(frame []byte) (ReadStored, error) {
	id, err := decodeID(frame, readStoredShape)
	return ReadStored{ID: id}, err
}

func decodeID(frame []byte, want shape) (string, error) {
	f, err := decode(frame, want)
	if err != nil {
		return "", err
	}
	return string(f[0]), nil
}

func encode(kind byte, fields ...[]byte) ([]byte, error) {
	frame := []byte{frameVersion, kind}
	for _, field := range fields {
		if len(field) > math.MaxUint16 {
			return nil, fmt.Errorf("%w: field too long", ErrProtocol)
		}
		frame = binary.BigEndian.AppendUint16(frame, uint16(len(field)))
		frame = append(frame, field...)
	}
	if len(frame) > MaxFrameBytes {
		return nil, fmt.Errorf("%w: frame too long", ErrProtocol)
	}
	return frame, nil
}

// decode splits a frame of the wanted shape into exactly its fields, each
// a copy.
func decode(frame []byte, want shape) ([][]byte, error) {
	got, err := Kind(frame)
	if err != nil {
		return nil, err
	}
	if got != want.kind {
		return nil, fmt.Errorf("%w: unexpected frame type %d", ErrProtocol, got)
	}
	rest := frame[2:]
	fields := make([][]byte, 0, want.fields)
	for range want.fields {
		var field []byte
		if field, rest, err = nextField(rest); err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("%w: trailing bytes", ErrProtocol)
	}
	return fields, nil
}

// nextField is one length-prefixed field, copied, and what follows it.
func nextField(rest []byte) ([]byte, []byte, error) {
	if len(rest) < 2 {
		return nil, nil, fmt.Errorf("%w: truncated frame", ErrProtocol)
	}
	size := int(binary.BigEndian.Uint16(rest))
	if len(rest)-2 < size {
		return nil, nil, fmt.Errorf("%w: truncated field", ErrProtocol)
	}
	return append([]byte(nil), rest[2:2+size]...), rest[2+size:], nil
}

// stamp is a time as messages carry and hash it.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
