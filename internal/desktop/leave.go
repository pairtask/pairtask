package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/pairtask/pairtask/internal/enroll"
	"github.com/pairtask/pairtask/internal/protocol"
)

type joinWorker struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func (n *Node) workerContext(space string) context.Context {
	n.joins.mu.Lock()
	defer n.joins.mu.Unlock()
	return n.joins.active[space].ctx
}

func (n *Node) stopSpaceFollower(ctx context.Context, space string) error {
	n.joins.mu.Lock()
	worker := n.joins.active[space]
	n.joins.mu.Unlock()
	if worker == nil {
		return nil
	}
	worker.cancel()
	select {
	case <-worker.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *Node) leaveSpace(ctx context.Context, args LeaveArgs) error {
	if args.Space != args.Confirm || len(args.Space) != 32 || strings.Trim(args.Space, "0123456789abcdef") != "" {
		return errors.New("confirm the full space ID")
	}
	if err := n.stopSpaceFollower(ctx, args.Space); err != nil {
		return err
	}
	lock, err := takeFollow(n.lockPath(args.Space))
	if err != nil {
		return err
	}
	saves := enroll.Saves(filepath.Dir(n.lockPath(args.Space)))
	_, err = n.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpLeave, Space: args.Space})
	if err != nil {
		lock.release()
		if kept, ok := saves.Load(args.Space); ok {
			n.spawn(func() { n.resumeJoin(kept) })
		}
		return err
	}
	defer lock.release()
	if err := os.Remove(saves.Path(args.Space)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	n.joins.mu.Lock()
	delete(n.joins.follow, args.Space)
	n.joins.mu.Unlock()
	return nil
}
