package messaging

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
)

// Version 5 carries where a review request is back to its requester
// (#235), in a phase of its own after the directories: the reviewer's side
// sends the latest outcome of each request it holds, the requester's side
// answers each, and a request's outcomes never travel as messages.

// OutcomeVersion is the first version that carries review outcomes.
const OutcomeVersion byte = 5

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
	outcomeStoredShape  = shape{FrameOutcomeStored, 2}
	outcomeRefusedShape = shape{FrameOutcomeRefused, 3}
)

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

func EncodeOutcome(o ReviewOutcome) ([]byte, error) {
	return encode(FrameReviewOutcome, []byte(o.AskID), []byte(o.Reviewer), binary.BigEndian.AppendUint64(nil, o.Seq),
		u32(o.Attempt), u32(o.Intent), []byte(o.State), []byte(o.Verdict), []byte(o.Summary), []byte(o.URL), []byte(o.Reason))
}

func DecodeOutcome(frame []byte) (ReviewOutcome, error) {
	f, err := decode(frame, outcomeShape)
	if err != nil {
		return ReviewOutcome{}, err
	}
	if len(f[2]) != 8 || len(f[3]) != 4 || len(f[4]) != 4 {
		return ReviewOutcome{}, fmt.Errorf("%w: outcome numbers", ErrProtocol)
	}
	return ReviewOutcome{AskID: string(f[0]), Reviewer: string(f[1]), Seq: binary.BigEndian.Uint64(f[2]),
		Attempt: binary.BigEndian.Uint32(f[3]), Intent: binary.BigEndian.Uint32(f[4]), State: string(f[5]),
		Verdict: string(f[6]), Summary: string(f[7]), URL: string(f[8]), Reason: string(f[9])}, nil
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
