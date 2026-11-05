package forge

import (
	"context"
	"errors"
)

// CommitChecker is an adapter that can say whether a commit exists in a
// target's repository (#304): false only when the forge answers that it
// doesn't; anything it can't tell is an error.
type CommitChecker interface {
	CommitExists(ctx context.Context, t Target, sha string) (bool, error)
}

// Pin check codes: a pinned commit the forge says doesn't exist, a head
// that isn't the change's head now, or no answer to tell.
const (
	PinHeadMissing = "head_missing"
	PinBaseMissing = "base_missing"
	PinHeadMoved   = "head_moved"
	PinNotChecked  = "not_checked"
)

// PinCheck is one thing a pin check found: Current is the change's head
// for head_moved, Class why for not_checked.
type PinCheck struct {
	Code, Current, Class string
}

// CheckPins checks pinned revisions against the change, advisory only: it
// reads the change first, so a commit the forge then can't find is
// authoritatively missing rather than out of reach. A base that exists but
// isn't the change's base is never flagged; pins are never changed.
func CheckPins(ctx context.Context, f Forge, t Target, pins Revisions) []PinCheck {
	checker, ok := f.(CommitChecker)
	if !ok {
		return []PinCheck{{Code: PinNotChecked, Class: ClassUnsupported}}
	}
	now, err := f.Resolve(ctx, t)
	if err != nil {
		return []PinCheck{notChecked(ctx, err)}
	}
	var out []PinCheck
	for _, pin := range []pinnedCommit{{pins.Head, PinHeadMissing}, {pins.Base, PinBaseMissing}} {
		if c, found := pin.check(ctx, checker, t); found {
			out = append(out, c)
		}
	}
	if pins.Head != now.Head && !missing(out, PinHeadMissing) {
		out = append(out, PinCheck{Code: PinHeadMoved, Current: now.Head})
	}
	return out
}

// pinnedCommit is one pin, and the code its absence gives.
type pinnedCommit struct{ sha, missing string }

// check is what the pin's lookup found, if anything.
func (p pinnedCommit) check(ctx context.Context, c CommitChecker, t Target) (PinCheck, bool) {
	exists, err := c.CommitExists(ctx, t, p.sha)
	switch {
	case err != nil:
		return notChecked(ctx, err), true
	case !exists:
		return PinCheck{Code: p.missing}, true
	}
	return PinCheck{}, false
}

func missing(checks []PinCheck, code string) bool {
	for _, c := range checks {
		if c.Code == code || c.Code == PinNotChecked {
			return true
		}
	}
	return false
}

// timedOut: the check's own time ran out, or the error says it did.
func timedOut(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// notChecked is a check that got no answer, with its class: timeout when
// the check's own time ran out, whatever the adapter made of it.
func notChecked(ctx context.Context, err error) PinCheck {
	class := ClassUnavailable
	var fe *Error
	switch {
	case timedOut(ctx, err):
		class = "timeout"
	case errors.As(err, &fe):
		class = fe.Class
	}
	return PinCheck{Code: PinNotChecked, Class: class}
}
