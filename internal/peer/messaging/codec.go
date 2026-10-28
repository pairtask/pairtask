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

// Protocol versions this side speaks, as Hello carries them.
const (
	MinVersion byte = 1
	MaxVersion byte = 1
)

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
	if len(frame) < 2 || len(frame) > MaxFrameBytes {
		return 0, fmt.Errorf("%w: frame size", ErrProtocol)
	}
	if frame[0] != frameVersion || frame[1] < FrameHello || frame[1] > FrameUnsupported {
		return 0, fmt.Errorf("%w: unknown frame version or type", ErrProtocol)
	}
	return frame[1], nil
}

func EncodeHello(h Hello) ([]byte, error) { return encode(FrameHello, []byte{h.Min}, []byte{h.Max}) }

func DecodeHello(frame []byte) (Hello, error) {
	min, max, err := decodeVersions(frame, FrameHello)
	return Hello{Min: min, Max: max}, err
}

func EncodeUnsupported(u Unsupported) ([]byte, error) {
	return encode(FrameUnsupported, []byte{u.Min}, []byte{u.Max})
}

func DecodeUnsupported(frame []byte) (Unsupported, error) {
	min, max, err := decodeVersions(frame, FrameUnsupported)
	return Unsupported{Min: min, Max: max}, err
}

func decodeVersions(frame []byte, kind byte) (byte, byte, error) {
	f, err := decode(frame, kind, 2)
	if err != nil {
		return 0, 0, err
	}
	if len(f[0]) != 1 || len(f[1]) != 1 || f[0][0] > f[1][0] {
		return 0, 0, fmt.Errorf("%w: versions", ErrProtocol)
	}
	return f[0][0], f[1][0], nil
}

func EncodeMessage(m Message) ([]byte, error) {
	n := binary.BigEndian.AppendUint64(nil, m.N)
	return encode(FrameMessage, []byte(m.ID), n, []byte(stamp(m.CreatedAt)), []byte(m.Text))
}

func DecodeMessage(frame []byte) (Message, error) {
	f, err := decode(frame, FrameMessage, 4)
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
	id, err := decodeID(frame, FrameStored)
	return Stored{ID: id}, err
}

func EncodeRefused(r Refused) ([]byte, error) {
	return encode(FrameRefused, []byte(r.ID), []byte(r.Class), []byte(r.Reason))
}

func DecodeRefused(frame []byte) (Refused, error) {
	f, err := decode(frame, FrameRefused, 3)
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
	_, err := decode(frame, FrameEnd, 0)
	return err
}

func EncodeRead(r Read) ([]byte, error) { return encode(FrameRead, []byte(r.ID)) }

func DecodeRead(frame []byte) (Read, error) {
	id, err := decodeID(frame, FrameRead)
	return Read{ID: id}, err
}

func EncodeReadStored(r ReadStored) ([]byte, error) { return encode(FrameReadStored, []byte(r.ID)) }

func DecodeReadStored(frame []byte) (ReadStored, error) {
	id, err := decodeID(frame, FrameReadStored)
	return ReadStored{ID: id}, err
}

func decodeID(frame []byte, kind byte) (string, error) {
	f, err := decode(frame, kind, 1)
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

// decode splits a frame of the wanted type into exactly count fields, each
// a copy.
func decode(frame []byte, kind byte, count int) ([][]byte, error) {
	got, err := Kind(frame)
	if err != nil {
		return nil, err
	}
	if got != kind {
		return nil, fmt.Errorf("%w: unexpected frame type %d", ErrProtocol, got)
	}
	rest := frame[2:]
	fields := make([][]byte, 0, count)
	for range count {
		if len(rest) < 2 {
			return nil, fmt.Errorf("%w: truncated frame", ErrProtocol)
		}
		size := int(binary.BigEndian.Uint16(rest))
		if len(rest)-2 < size {
			return nil, fmt.Errorf("%w: truncated field", ErrProtocol)
		}
		fields = append(fields, append([]byte(nil), rest[2:2+size]...))
		rest = rest[2+size:]
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("%w: trailing bytes", ErrProtocol)
	}
	return fields, nil
}

// stamp is a time as messages carry and hash it.
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
