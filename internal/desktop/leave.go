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
	if !args.confirmed() {
		return errors.New("confirm the full space ID")
	}
	if err := n.stopSpaceFollower(ctx, args.Space); err != nil {
		return err
	}
	lock, err := takeFollow(n.lockPath(args.Space))
	if err != nil {
		return err
	}
	return n.leaveLocked(ctx, args.Space, lock)
}

func (args LeaveArgs) confirmed() bool {
	return args.Space == args.Confirm && len(args.Space) == 32 && strings.Trim(args.Space, "0123456789abcdef") == ""
}

func (n *Node) leaveLocked(ctx context.Context, space string, lock *followLock) error {
	saves := enroll.Saves(filepath.Dir(n.lockPath(space)))
	_, err := n.space(ctx, protocol.SpaceRequest{Op: protocol.SpaceOpLeave, Space: space})
	if err != nil {
		lock.release()
		if kept, ok := saves.Load(space); ok {
			n.spawn(func() { n.resumeJoin(kept) })
		}
		return err
	}
	defer lock.release()
	return n.forgetEnrollment(saves, space)
}

func (n *Node) forgetEnrollment(saves enroll.Saves, space string) error {
	if err := os.Remove(saves.Path(space)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	n.joins.mu.Lock()
	delete(n.joins.follow, space)
	n.joins.mu.Unlock()
	return nil
}
