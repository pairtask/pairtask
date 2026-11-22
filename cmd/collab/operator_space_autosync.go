package main

import (
	"context"
	"sync"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

// While the operator chat is open on the machine that holds the person
// key, every space this person owns is synced in the background: members'
// requests are applied and the map is renewed before it lapses, as
// `pairtask space sync` would. Joins still wait for an explicit approve.
// Nothing is printed, so the chat's screen is left alone; with no keys or
// no anchor there is nothing to do.

// spaceAutoSyncEvery is how often an open chat syncs owned spaces.
const spaceAutoSyncEvery = 5 * time.Minute

// spaceAutoSync syncs owned spaces until stopped.
type spaceAutoSync struct {
	call   operatorCall
	every  time.Duration
	cancel context.CancelFunc
	done   sync.WaitGroup
}

// startSpaceAutoSync starts syncing now and then every interval; stop ends
// it and waits.
func startSpaceAutoSync(ctx context.Context, call operatorCall, every time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	a := &spaceAutoSync{call: call, every: every, cancel: cancel}
	a.done.Add(1)
	go a.loop(ctx)
	return func() {
		a.cancel()
		a.done.Wait()
	}
}

func (a *spaceAutoSync) loop(ctx context.Context) {
	defer a.done.Done()
	tick := time.NewTicker(a.every)
	defer tick.Stop()
	for {
		a.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// pass syncs each pinned space this person owns; any failure waits for the
// next pass. Each pass loads the keys afresh, so a key or anchor removed
// or replaced on this machine since the last one is never signed with.
func (a *spaceAutoSync) pass(ctx context.Context) {
	s := &pairbookSession{call: a.call}
	me, err := s.person()
	if err != nil {
		return // no keys or no anchor here: not an owner's machine
	}
	pins, err := owned(ctx, s, me.KeyID())
	if err != nil {
		return
	}
	for _, p := range pins {
		if ctx.Err() != nil {
			return
		}
		_, _ = s.foldSpace(ctx, p.Space, "")
	}
}

// owned are the pinned spaces whose owner is the person key ownerID.
func owned(ctx context.Context, s *pairbookSession, ownerID string) ([]protocol.SpacePin, error) {
	res, err := s.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpList})
	if err != nil {
		return nil, err
	}
	var out []protocol.SpacePin
	for _, p := range res.Pins {
		if p.Owner == ownerID {
			out = append(out, p)
		}
	}
	return out, nil
}
