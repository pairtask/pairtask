package messaging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Identity statements (#392, #421 slice 1), version 13: before any
// message of a session, each side sends the signed identity statements it
// owes the other (records, revocations, successions, handovers, notices),
// each answered with what the receiver did with it. Running first means a
// revocation is always judged before a message from the device it
// revokes; a frame of another phase arriving in this one is a protocol
// error, so no peer can put a message ahead of it.

const (
	// IdentityVersion is the first version that carries identity statements.
	IdentityVersion byte = 13
	// FrameIdentity is one signed identity statement, as marshalled.
	FrameIdentity byte = 34
	// FrameIdentityStored answers it: the statement's digest and what the
	// receiver did with it.
	FrameIdentityStored byte = 35
	// FrameIdentityMore ends a side's identity batch, as End does, and says
	// more statements are owed: the round then carries nothing else.
	FrameIdentityMore byte = 36
	// MaxIdentityBytes bounds one statement.
	MaxIdentityBytes = 16 << 10
)

// Identity effects as the receiver reports them.
const (
	IdentityApplied   = "applied"
	IdentityDuplicate = "duplicate"
	IdentityParked    = "parked"
	IdentityFrozen    = "frozen"
	IdentityRejected  = "rejected"
)

var identityEffects = map[string]bool{IdentityApplied: true, IdentityDuplicate: true, IdentityParked: true, IdentityFrozen: true, IdentityRejected: true}

// IdentityStatement is one statement this side owes a contact.
type IdentityStatement struct{ Body []byte }

// Digest names a statement on the wire: the SHA-256 of its bytes.
func (s IdentityStatement) Digest() string {
	sum := sha256.Sum256(s.Body)
	return hex.EncodeToString(sum[:])
}

// IdentityStored is the receiver's answer to one statement.
type IdentityStored struct {
	Digest, Effect string
}

// IdentityStore is what an IdentityVersion session needs besides Store.
type IdentityStore interface {
	Store
	// PendingIdentity is up to max statements still owed contact, oldest first.
	PendingIdentity(ctx context.Context, contact string, max int) ([]IdentityStatement, error)
	// IdentityAnswered records the contact's answer to one of them: any
	// answer settles it.
	IdentityAnswered(ctx context.Context, contact string, s IdentityStored) error
	// AcceptIdentity judges a statement from contact and keeps it when it
	// had an effect; the effect is one of the Identity* constants.
	AcceptIdentity(ctx context.Context, contact string, body []byte, at time.Time) (string, error)
}

func (s Session) identity() (IdentityStore, bool) {
	if s.Version < IdentityVersion {
		return nil, false
	}
	is, ok := s.Store.(IdentityStore)
	return is, ok
}

var (
	identityMoreShape   = shape{FrameIdentityMore, 0}
	identityShape       = shape{FrameIdentity, 1}
	identityStoredCodec = fieldCodec[IdentityStored]{shape{FrameIdentityStored, 2}, "identity answer",
		func(a IdentityStored) []string { return []string{a.Digest, a.Effect} },
		func(f []string) IdentityStored { return IdentityStored{Digest: f[0], Effect: f[1]} },
		func(a IdentityStored) bool { return len(a.Digest) == 64 && identityEffects[a.Effect] }}
)

func EncodeIdentity(s IdentityStatement) ([]byte, error) {
	if len(s.Body) == 0 || len(s.Body) > MaxIdentityBytes {
		return nil, fmt.Errorf("%w: identity statement size", ErrProtocol)
	}
	return encode(FrameIdentity, s.Body)
}

func DecodeIdentity(frame []byte) (IdentityStatement, error) {
	f, err := decode(frame, identityShape)
	if err != nil {
		return IdentityStatement{}, err
	}
	if len(f[0]) == 0 || len(f[0]) > MaxIdentityBytes {
		return IdentityStatement{}, fmt.Errorf("%w: identity statement size", ErrProtocol)
	}
	return IdentityStatement{Body: f[0]}, nil
}

func EncodeIdentityStored(a IdentityStored) ([]byte, error) { return identityStoredCodec.encode(a) }

func DecodeIdentityStored(frame []byte) (IdentityStored, error) {
	return identityStoredCodec.decode(frame)
}

// sendIdentity sends this side's owed statements, each answered before the
// next, then End.
func (s Session) sendIdentity(ctx context.Context, r *Result) error {
	is, _ := s.identity()
	pending, err := is.PendingIdentity(ctx, s.Contact, SessionMessages+1)
	if err != nil {
		return err
	}
	more := len(pending) > SessionMessages
	for _, st := range pending[:min(len(pending), SessionMessages)] {
		if err := s.sendStatement(ctx, is, st); err != nil {
			return err
		}
		r.IdentitySent++
	}
	if more {
		r.IdentityMore = true
		return s.send(encode(FrameIdentityMore))
	}
	return s.send(EncodeEnd())
}

func (s Session) sendStatement(ctx context.Context, is IdentityStore, st IdentityStatement) error {
	if err := s.send(EncodeIdentity(st)); err != nil {
		return err
	}
	frame, err := s.Frames.Recv()
	if err != nil {
		return err
	}
	answer, err := DecodeIdentityStored(frame)
	if err != nil {
		return err
	}
	if answer.Digest != st.Digest() {
		return fmt.Errorf("%w: answer for another identity statement", ErrProtocol)
	}
	return is.IdentityAnswered(ctx, s.Contact, answer)
}

// receiveIdentity takes the peer's statements, answering each, until its
// End; any other frame here is a protocol error.
func (s Session) receiveIdentity(ctx context.Context, r *Result) error {
	is, _ := s.identity()
	for count := 0; ; count++ {
		frame, err := s.Frames.Recv()
		if err != nil {
			return err
		}
		switch kind, _ := Kind(frame); kind {
		case FrameEnd:
			return DecodeEnd(frame)
		case FrameIdentityMore:
			r.IdentityMore = true
			_, err := decode(frame, identityMoreShape)
			return err
		}
		if count >= SessionMessages {
			return fmt.Errorf("%w: more identity statements than a session carries", ErrProtocol)
		}
		if err := s.answerStatement(ctx, is, frame, r); err != nil {
			return err
		}
	}
}

func (s Session) answerStatement(ctx context.Context, is IdentityStore, frame []byte, r *Result) error {
	st, err := DecodeIdentity(frame)
	if err != nil {
		return err
	}
	effect, err := is.AcceptIdentity(ctx, s.Contact, st.Body, s.Now())
	if err != nil {
		return err
	}
	if !identityEffects[effect] {
		return fmt.Errorf("identity store gave unknown effect %q", effect)
	}
	r.IdentityReceived++
	return s.send(EncodeIdentityStored(IdentityStored{Digest: st.Digest(), Effect: effect}))
}

// identitySessionStore marks the identity half's store errors too.
type identitySessionStore struct {
	claimSessionStore
	is IdentityStore
}

func (s identitySessionStore) PendingIdentity(ctx context.Context, contact string, max int) ([]IdentityStatement, error) {
	out, err := s.is.PendingIdentity(ctx, contact, max)
	return out, storeErr(err)
}

func (s identitySessionStore) IdentityAnswered(ctx context.Context, contact string, a IdentityStored) error {
	return storeErr(s.is.IdentityAnswered(ctx, contact, a))
}

func (s identitySessionStore) AcceptIdentity(ctx context.Context, contact string, body []byte, at time.Time) (string, error) {
	e, err := s.is.AcceptIdentity(ctx, contact, body, at)
	return e, storeErr(err)
}
