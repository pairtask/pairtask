package firstcontact

import (
	"context"
	"errors"
	"sync"
	"time"
)

// guarded runs one blocking operation under ctx: ctx's deadline becomes the
// operation's deadline, set through set, and cancelling ctx forces it into
// the past. Afterwards the deadline is cleared, never racing a late
// cancellation, so the next operation starts clean.
func guarded(ctx context.Context, set func(time.Time) error, op func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g := &deadlineGuard{set: set}
	deadline, _ := ctx.Deadline()
	g.apply(deadline)
	stop := context.AfterFunc(ctx, g.expire)
	err := op()
	stop()
	g.finish()
	if err != nil && ctx.Err() != nil {
		return errors.Join(ctx.Err(), err)
	}
	return err
}

// deadlineGuard serialises a cancellation with the end of its operation.
type deadlineGuard struct {
	mu   sync.Mutex
	done bool
	set  func(time.Time) error
}

func (g *deadlineGuard) apply(t time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	_ = g.set(t)
}

// expire interrupts the operation unless it already finished.
func (g *deadlineGuard) expire() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.done {
		_ = g.set(time.Unix(1, 0))
	}
}

// finish clears the deadline; a later expire does nothing.
func (g *deadlineGuard) finish() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.done = true
	_ = g.set(time.Time{})
}
