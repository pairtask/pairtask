package messaging

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
	"unicode"
	"unicode/utf8"
)

// Version 5 carries where a review request is back to its requester
// (#235), in a phase of its own after the directories: the reviewer's side
// sends the latest outcome of each request it holds, the requester's side
// answers each, and a request's outcomes never travel as messages.

// OutcomeVersion is the first version that carries review outcomes;
// ReviewIDVersion the first whose outcomes also name the reviewer's own
// review, once there is one (#259).
const (
	OutcomeVersion  byte = 5
	ReviewIDVersion byte = 6
)

// ReviewOutcome is where a review request is, as its reviewer's side says:
// for the request AskID asked of Reviewer, the Seq'th outcome, with the
// review's attempt and publication intent, its state, and once there is
// one the verdict, a summary, the published URL, and why it ended.
type ReviewOutcome struct {
	AskID, Reviewer string
	Seq             uint64
	Attempt, Intent uint32
	State, Verdict  string
	Summary, URL    string
	Reason          string
	// ReviewID is the reviewer's side's own ID for the review, "" until it
	// has one or from a peer below ReviewIDVersion. It is not part of the
	// outcome's identity (Digest), so the same outcome sent at either
	// version is the same outcome; read as it is sent, never stored with
	// the outcome.
	ReviewID string `json:"-"`
}

// Bounds of an outcome's text.
const (
	OutcomeSummaryBytes = 4 << 10
	OutcomeFieldBytes   = 512
)

// Digest identifies an outcome's content: the same request and Seq with
// another digest is a conflict.
func (o ReviewOutcome) Digest() [32]byte {
	h := sha256.New()
	for _, part := range []string{o.AskID, o.Reviewer, strconv.FormatUint(o.Seq, 10), strconv.FormatUint(uint64(o.Attempt), 10),
		strconv.FormatUint(uint64(o.Intent), 10), o.State, o.Verdict, o.Summary, o.URL, o.Reason} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		h.Write(size[:])
		h.Write([]byte(part))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// OutcomeAnswer is how the requester's side answered an outcome: Stored,
// or refused for good, or held back for now (Transient), when it can't take
// it yet; the reviewer's side keeps a held one and offers it again.
type OutcomeAnswer struct {
	Stored    bool
	Transient bool
	Reason    string
}

var (
	outcomeShape        = shape{FrameReviewOutcome, 10}
	outcomeIDShape      = shape{FrameReviewOutcome, 11}
	outcomeStoredShape  = shape{FrameOutcomeStored, 2}
	outcomeRefusedShape = shape{FrameOutcomeRefused, 3}
)

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

// EncodeOutcome is an outcome as version 5 sends it, without its review ID.
func EncodeOutcome(o ReviewOutcome) ([]byte, error) {
	return encode(FrameReviewOutcome, outcomeFields(o)...)
}

// EncodeOutcomeFor is an outcome as the agreed version sends it: from
// ReviewIDVersion with the reviewer's review ID last, "" while there is
// none.
func EncodeOutcomeFor(o ReviewOutcome, version byte) ([]byte, error) {
	if version < ReviewIDVersion {
		return EncodeOutcome(o)
	}
	if o.ReviewID != "" && !ValidReviewID(o.ReviewID) {
		return nil, fmt.Errorf("%w: outcome review ID", ErrProtocol)
	}
	return encode(FrameReviewOutcome, append(outcomeFields(o), []byte(o.ReviewID))...)
}

// ReviewIDBytes bounds a review ID.
const ReviewIDBytes = 128

// ValidReviewID reports a review ID as the broker takes one
// (protocol.ValidateReviewID, which this package can't import): 1 to
// ReviewIDBytes of UTF-8 without control characters.
func ValidReviewID(id string) bool {
	if id == "" || len(id) > ReviewIDBytes || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func outcomeFields(o ReviewOutcome) [][]byte {
	return [][]byte{[]byte(o.AskID), []byte(o.Reviewer), binary.BigEndian.AppendUint64(nil, o.Seq),
		u32(o.Attempt), u32(o.Intent), []byte(o.State), []byte(o.Verdict), []byte(o.Summary), []byte(o.URL), []byte(o.Reason)}
}

// DecodeOutcome reads a version 5 outcome.
func DecodeOutcome(frame []byte) (ReviewOutcome, error) {
	return decodeOutcome(frame, outcomeShape)
}

// DecodeOutcomeFor reads an outcome as the agreed version sends it, each
// shape strictly: version 5's has no review ID, ReviewIDVersion's always
// one, "" or a valid ID.
func DecodeOutcomeFor(frame []byte, version byte) (ReviewOutcome, error) {
	if version < ReviewIDVersion {
		return DecodeOutcome(frame)
	}
	o, err := decodeOutcome(frame, outcomeIDShape)
	if err == nil && o.ReviewID != "" && !ValidReviewID(o.ReviewID) {
		return ReviewOutcome{}, fmt.Errorf("%w: outcome review ID", ErrProtocol)
	}
	return o, err
}

func decodeOutcome(frame []byte, want shape) (ReviewOutcome, error) {
	f, err := decode(frame, want)
	if err != nil {
		return ReviewOutcome{}, err
	}
	if !fixedWidths(f[2:5], 8, 4, 4) {
		return ReviewOutcome{}, fmt.Errorf("%w: outcome numbers", ErrProtocol)
	}
	o := ReviewOutcome{AskID: string(f[0]), Reviewer: string(f[1]), Seq: binary.BigEndian.Uint64(f[2]),
		Attempt: binary.BigEndian.Uint32(f[3]), Intent: binary.BigEndian.Uint32(f[4]), State: string(f[5]),
		Verdict: string(f[6]), Summary: string(f[7]), URL: string(f[8]), Reason: string(f[9])}
	if len(f) > 10 {
		o.ReviewID = string(f[10])
	}
	return o, nil
}

// fixedWidths: each field exactly its width in bytes.
func fixedWidths(fields [][]byte, widths ...int) bool {
	for i, w := range widths {
		if len(fields[i]) != w {
			return false
		}
	}
	return true
}

// OutcomeReply answers one outcome: Stored, or Refused with why.
type OutcomeReply struct {
	AskID  string
	Seq    uint64
	Reason string // "" is Stored
}

func EncodeOutcomeReply(r OutcomeReply) ([]byte, error) {
	seq := binary.BigEndian.AppendUint64(nil, r.Seq)
	if r.Reason == "" {
		return encode(FrameOutcomeStored, []byte(r.AskID), seq)
	}
	return encode(FrameOutcomeRefused, []byte(r.AskID), seq, []byte(r.Reason))
}

func DecodeOutcomeReply(frame []byte) (OutcomeReply, error) {
	kind, err := Kind(frame)
	if err != nil {
		return OutcomeReply{}, err
	}
	want := outcomeStoredShape
	if kind == FrameOutcomeRefused {
		want = outcomeRefusedShape
	}
	f, err := decode(frame, want)
	if err != nil {
		return OutcomeReply{}, err
	}
	if len(f[1]) != 8 {
		return OutcomeReply{}, fmt.Errorf("%w: outcome seq", ErrProtocol)
	}
	r := OutcomeReply{AskID: string(f[0]), Seq: binary.BigEndian.Uint64(f[1])}
	if kind == FrameOutcomeRefused {
		r.Reason = string(f[2])
	}
	return r, nil
}
