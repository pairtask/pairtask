package hub

import (
	"context"
	"crypto"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

type addressRecorder struct {
	*retryDirectory
	current pairbook.SignedSpaceMap
	writes  []time.Duration
}

func (d *addressRecorder) Map(context.Context, space.Pin) (pairbook.SignedSpaceMap, error) {
	return d.current, nil
}

func (d *addressRecorder) PublishAddress(_ context.Context, _ space.Pin, endpoints []string, valid time.Duration) error {
	if !slices.Equal(endpoints, []string{"mac.example:9443"}) {
		return errors.New("wrong endpoints")
	}
	d.writes = append(d.writes, valid)
	return nil
}

func TestSpaceAddressRenewalStopsAfterUnpin(t *testing.T) {
	f := newRetryFixture(t)
	now := time.Now().Truncate(time.Millisecond)
	clock := realSpaceTiming()
	clock.Now = func() time.Time { return now }
	f.h.SetSpaceTiming(clock)
	directory := &addressRecorder{retryDirectory: f.d, current: f.m}
	f.h.SetSpaces(SpaceConfig{Clock: clock.Now, Maps: func(string, crypto.Signer) (space.MapDirectory, error) { return directory, nil }})
	ctx := context.Background()
	messageWorkerOK(t, f.h.store.PinSpace(ctx, f.pin))
	messageWorkerOK(t, f.h.KeepSpaceMap(ctx, f.pin.Space, f.m))
	const valid = time.Hour
	req := protocol.SpaceRequest{Op: protocol.SpaceOpMapAddress, Space: f.pin.Space, Addresses: []string{"mac.example:9443"}, ValidFor: valid.Milliseconds()}
	messageWorkerOK(t, runMapOp(t, f.h, req, nil))
	now = now.Add(valid/2 - time.Second)
	f.h.refreshPass(ctx)
	if len(directory.writes) != 1 {
		t.Fatalf("got %d writes before half-life", len(directory.writes))
	}
	now = now.Add(2 * time.Second)
	f.h.refreshPass(ctx)
	if len(directory.writes) != 2 {
		t.Fatalf("got %d writes after half-life", len(directory.writes))
	}
	if directory.writes[1] != valid {
		t.Fatal("renewal changed the validity interval")
	}
	messageWorkerOK(t, f.h.unpinSpace(ctx, spaceKey(f.pin.Space)))
	now = now.Add(valid)
	f.h.refreshPass(ctx)
	if len(directory.writes) != 2 {
		t.Fatal("unpinning left address publication active")
	}
}

func TestSpaceAddressUnpinBetweenRequestAndWorker(t *testing.T) {
	f := newRetryFixture(t)
	directory := &addressRecorder{retryDirectory: f.d, current: f.m}
	f.h.SetSpaces(SpaceConfig{Maps: func(string, crypto.Signer) (space.MapDirectory, error) { return directory, nil }})
	ctx := context.Background()
	messageWorkerOK(t, f.h.store.PinSpace(ctx, f.pin))
	messageWorkerOK(t, f.h.KeepSpaceMap(ctx, f.pin.Space, f.m))
	req := protocol.SpaceRequest{Op: protocol.SpaceOpMapAddress, Space: f.pin.Space, Addresses: []string{"mac.example:9443"}}
	step, refusal := spaceOps[req.Op](f.h, ctx, req)
	if refusal != nil || step.job == nil {
		t.Fatalf("bound address request refused at dispatch: %v", refusal)
	}
	messageWorkerOK(t, f.h.unpinSpace(ctx, spaceKey(f.pin.Space)))
	if _, err := step.job(ctx, ctx); err == nil {
		t.Fatal("unpin before worker still allowed publication")
	}
	_, found, err := f.h.store.SpaceAddressOf(ctx, f.pin.Space)
	messageWorkerOK(t, err)
	if found {
		t.Fatal("unpin before worker left address configured")
	}
}
