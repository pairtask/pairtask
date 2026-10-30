package messaging

import (
	"fmt"
	"unicode/utf8"
)

// Version 4 carries review requests (#235): one of the sender's agents, or
// its operator, asks one of the receiver's agents to review a change pinned
// to exact revisions. The receiver decides whether it may.

// ReviewVersion is the first version that carries review requests.
const ReviewVersion byte = 4

// ReviewAsk is what a review request asks: a change, pinned, what to focus
// on, and whether the requester wants the verdict published, formally.
// The receiver allows at most that.
type ReviewAsk struct {
	Forge, Repository, Change string
	Scope                     string
	Base, Head                string
	Publish, Formal           bool
}

// Bounds of a review request's fields.
const (
	ReviewFieldBytes = 256     // forge, repository, change, base, head
	ReviewScopeBytes = 1 << 10 // what to focus on
)

// parts are the request's fields as Hash covers them.
func (r *ReviewAsk) parts() []string {
	return []string{r.Forge, r.Repository, r.Change, r.Scope, r.Base, r.Head, r.flags()}
}

func (r *ReviewAsk) flags() string { return string(rune('0' + r.flagByte())) }

func (r *ReviewAsk) flagByte() byte {
	var b byte
	if r.Publish {
		b |= 1
	}
	if r.Formal {
		b |= 2
	}
	return b
}

// ValidReviewAsk: a request for exactly one agent, answering nothing, with
// every field of its change and both pins set and within bounds.
func ValidReviewAsk(m Message) bool {
	r := m.Review
	if r == nil || len(m.To) != 1 || m.ReplyTo.ID != "" {
		return false
	}
	for _, f := range []string{r.Forge, r.Repository, r.Change, r.Base, r.Head} {
		if f == "" || len(f) > ReviewFieldBytes || !utf8.ValidString(f) {
			return false
		}
	}
	return len(r.Scope) <= ReviewScopeBytes && utf8.ValidString(r.Scope)
}

var reviewAskShape = shape{FrameReviewAsk, 15}

// EncodeReviewAsk is a routed message's fields, then the request's.
func EncodeReviewAsk(m Message) ([]byte, error) {
	routed, err := EncodeRouted(m)
	if err != nil {
		return nil, err
	}
	f, err := decode(routed, routedShape)
	if err != nil {
		return nil, err
	}
	r := m.Review
	return encode(FrameReviewAsk, append(f, []byte(r.Forge), []byte(r.Repository), []byte(r.Change), []byte(r.Scope),
		[]byte(r.Base), []byte(r.Head), []byte{r.flagByte()})...)
}

func DecodeReviewAsk(frame []byte) (Message, error) {
	f, err := decode(frame, reviewAskShape)
	if err != nil {
		return Message{}, err
	}
	routed, err := encode(FrameRouted, f[:8]...)
	if err != nil {
		return Message{}, err
	}
	m, err := DecodeRouted(routed)
	if err != nil {
		return Message{}, err
	}
	if len(f[14]) != 1 || f[14][0] > 3 {
		return Message{}, fmt.Errorf("%w: review flags", ErrProtocol)
	}
	m.Review = &ReviewAsk{Forge: string(f[8]), Repository: string(f[9]), Change: string(f[10]), Scope: string(f[11]),
		Base: string(f[12]), Head: string(f[13]), Publish: f[14][0]&1 != 0, Formal: f[14][0]&2 != 0}
	return m, nil
}
