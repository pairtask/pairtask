// Package enroll follows an enrollment of this workspace into an
// organization's Pairbook space, for the host CLI and for Desktop: start it
// (or pick up one kept from before), sign the person's join tied to its
// challenge, and follow it until it is admitted, denied or expired, signing
// the join again when the space's map moves. Only the base and the
// signatures change between sends.
package enroll

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/internal/spaceowner"
	"github.com/pairtask/pairtask/pairbook"
)

// Caller makes one space request of this workspace's broker.
type Caller func(context.Context, protocol.SpaceRequest) (*protocol.SpaceResult, error)

// Flow is one enrollment as this host follows it.
type Flow struct {
	Call  Caller
	Me    spaceowner.Me
	Link  string
	Saves Saves
	Poll  time.Duration // between checks
	Wait  time.Duration // waiting for an admin, before Follow returns Waiting

	Enroll protocol.SpaceEnroll
	Name   string
	Resume bool // picked up from Saves
	own    pairbook.Binding
	since  time.Time
}

// Outcomes Follow ends with besides an error.
const (
	Admitted = space.EnrollAdmitted
	Waiting  = "waiting" // confirmed, still waiting for an admin after Wait; kept to resume
)

var (
	ErrDenied  = errors.New("an admin declined this enrollment")
	ErrExpired = errors.New("the enrollment expired")
)

// Begin picks up the enrollment kept for the link's space, or starts one
// as name, and sends the join; it returns the state to follow from.
func (f *Flow) Begin(ctx context.Context, name string) (protocol.SpaceEnrollState, error) {
	l, err := space.ParseOrgLink(f.Link)
	if err != nil {
		return protocol.SpaceEnrollState{}, err
	}
	f.since = time.Now()
	if kept, ok := f.Saves.Load(l.Space); ok && kept.Link == f.Link {
		return f.resume(ctx, kept)
	}
	return f.start(ctx, name)
}

func (f *Flow) start(ctx context.Context, name string) (protocol.SpaceEnrollState, error) {
	res, err := f.Call(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapEnroll, Link: f.Link, Name: name})
	if err != nil {
		return protocol.SpaceEnrollState{}, err
	}
	if res.Enroll == nil {
		return protocol.SpaceEnrollState{}, errors.New("the broker didn't start an enrollment")
	}
	f.Enroll, f.Name = *res.Enroll, name
	if err := f.bind(ctx); err != nil {
		return protocol.SpaceEnrollState{}, err
	}
	return f.Send(ctx, f.Enroll.Base)
}

// resume follows an enrollment this host started before. A failure to
// check it keeps it: the Pairbook or broker may only be unreachable now.
func (f *Flow) resume(ctx context.Context, kept Saved) (protocol.SpaceEnrollState, error) {
	f.Enroll, f.Name, f.Resume = kept.Enroll, kept.Name, true
	if err := f.bind(ctx); err != nil {
		return protocol.SpaceEnrollState{}, err
	}
	st, err := f.state(ctx)
	if err != nil {
		return st, fmt.Errorf("couldn't check the enrollment started earlier; it is kept, try again later: %w", err)
	}
	return st, nil
}

// bind gets this workspace's acceptance of the person's binding.
func (f *Flow) bind(ctx context.Context) error {
	epoch, err := f.Me.Epoch()
	if err != nil {
		return err
	}
	now := time.Now().Truncate(time.Millisecond)
	res, err := f.Call(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapAccept, Lineage: pairbook.LineageHex(f.Me.Lineage), Epoch: epoch, Person: f.Me.KeyID(), At: now})
	if err != nil {
		return err
	}
	f.own = pairbook.Binding{Claim: f.Me.Claim(res.WorkspaceKey, epoch, now), Acceptance: res.Acceptance, WorkspaceKey: res.WorkspaceKey}
	return nil
}

// Fingerprint is this workspace's key as the person compares it.
func (f *Flow) Fingerprint() string { return Fingerprint(pairbook.KeyID(f.own.WorkspaceKey)) }

// Follow follows the enrollment from st until it is admitted (Admitted),
// waiting for an admin past Wait (Waiting, kept to resume), or ends with
// ErrDenied or ErrExpired.
func (f *Flow) Follow(ctx context.Context, st protocol.SpaceEnrollState) (string, error) {
	for {
		out, done, err := f.settled(st)
		if done || err != nil {
			return out, err
		}
		if st, err = f.next(ctx, st); err != nil {
			return "", err
		}
	}
}

func (f *Flow) settled(st protocol.SpaceEnrollState) (string, bool, error) {
	switch st.State {
	case space.EnrollAdmitted:
		f.Saves.Drop(f.Enroll.Space)
		return Admitted, true, nil
	case space.EnrollDenied:
		f.Saves.Drop(f.Enroll.Space)
		return "", true, ErrDenied
	case space.EnrollExpired:
		f.Saves.Drop(f.Enroll.Space)
		return "", true, ErrExpired
	case space.EnrollAwaitingApproval, space.EnrollStale:
		return f.waiting()
	case space.EnrollPendingSignIn:
		return "", false, f.confirmInTime()
	}
	return "", true, fmt.Errorf("the Pairbook reported an unknown enrollment state %q", st.State)
}

// waiting keeps waiting for an admin until Wait has passed, then keeps the
// enrollment to resume.
func (f *Flow) waiting() (string, bool, error) {
	if time.Since(f.since) <= f.Wait {
		return "", false, nil
	}
	if err := f.Saves.Save(Saved{Link: f.Link, Name: f.Name, Enroll: f.Enroll}); err != nil {
		return "", true, fmt.Errorf("an admin hasn't approved yet, and this enrollment couldn't be kept to resume: %w", err)
	}
	return Waiting, true, nil
}

func (f *Flow) confirmInTime() error {
	if time.Now().After(f.Enroll.ExpiresAt.Add(time.Minute)) {
		return ErrExpired
	}
	return nil
}

// next waits Poll, then signs a stale join again or checks the state.
func (f *Flow) next(ctx context.Context, st protocol.SpaceEnrollState) (protocol.SpaceEnrollState, error) {
	select {
	case <-ctx.Done():
		return st, ctx.Err()
	case <-time.After(f.Poll):
	}
	if st.State != space.EnrollStale {
		return f.state(ctx)
	}
	if st.Base == nil {
		return st, errors.New("the Pairbook asked for the join again without saying on which map")
	}
	return f.Send(ctx, *st.Base)
}

func (f *Flow) state(ctx context.Context) (protocol.SpaceEnrollState, error) {
	res, err := f.Call(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapEnrollState, Space: f.Enroll.Space, Enrollment: f.Enroll.Enrollment})
	return stateOf(res, err)
}

// Send signs the join on base and sends it.
func (f *Flow) Send(ctx context.Context, base protocol.SpaceMark) (protocol.SpaceEnrollState, error) {
	mark, err := MarkOf(base)
	if err != nil {
		return protocol.SpaceEnrollState{}, err
	}
	ch := f.Me.Change(spaceowner.Request{Space: f.Enroll.Space, Action: pairbook.ChangeJoin, Base: mark, Name: f.Name,
		TokenHash: sha256.Sum256(f.Enroll.Challenge), Binding: f.own}, time.Now().Truncate(time.Millisecond))
	record, err := json.Marshal(ch)
	if err != nil {
		return protocol.SpaceEnrollState{}, err
	}
	res, err := f.Call(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpMapEnrollSend, Space: f.Enroll.Space, Enrollment: f.Enroll.Enrollment, Record: record})
	return stateOf(res, err)
}

func stateOf(res *protocol.SpaceResult, err error) (protocol.SpaceEnrollState, error) {
	if err != nil {
		return protocol.SpaceEnrollState{}, err
	}
	if res.EnrollState == nil {
		return protocol.SpaceEnrollState{}, errors.New("the broker didn't report the enrollment's state")
	}
	return *res.EnrollState, nil
}

// MarkOf reads a mark the broker reported.
func MarkOf(m protocol.SpaceMark) (pairbook.Mark, error) {
	out := pairbook.Mark{Version: m.Version}
	d, err := hex.DecodeString(m.Digest)
	if err != nil || len(d) != len(out.Digest) {
		return out, errors.New("the broker reported a malformed map mark")
	}
	copy(out.Digest[:], d)
	return out, nil
}

// Fingerprint is a key ID as people compare it: its first 16 hex digits.
func Fingerprint(keyID string) string {
	h := strings.TrimPrefix(keyID, "sha256:")
	if len(h) > 16 {
		h = h[:16]
	}
	return h
}

// Saved is an enrollment kept to resume.
type Saved struct {
	Link   string               `json:"link"`
	Name   string               `json:"name"`
	Enroll protocol.SpaceEnroll `json:"enroll"`
}

// Saves is the folder that keeps enrollments waiting for an admin; ""
// keeps none.
type Saves string

// Path is where space's kept enrollment is.
func (d Saves) Path(space string) string { return filepath.Join(string(d), space+".json") }

// Load is the enrollment kept for space.
func (d Saves) Load(space string) (Saved, bool) {
	var e Saved
	if d == "" {
		return e, false
	}
	b, err := os.ReadFile(d.Path(space))
	if err != nil {
		return e, false
	}
	if json.Unmarshal(b, &e) != nil {
		return e, false
	}
	return e, e.Enroll.Space == space
}

// Save keeps e (0600, in a 0700 folder).
func (d Saves) Save(e Saved) error {
	if d == "" {
		return errors.New("no place to keep it")
	}
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return os.WriteFile(d.Path(e.Enroll.Space), b, 0o600)
}

// Drop forgets space's kept enrollment.
func (d Saves) Drop(space string) {
	if d != "" {
		_ = os.Remove(d.Path(space))
	}
}
