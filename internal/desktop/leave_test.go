package desktop

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/enroll"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
)

func TestLeaveRetainsEnrollmentOnRefusalAndForgetsItOnSuccess(t *testing.T) {
	dir := t.TempDir()
	fixture := &desktopSpaceDirectory{revokeErr: errors.New("directory unavailable")}
	n := spacesNode(t, filepath.Join(dir, "test.db"), fixture)
	n.cfg.Dir = dir
	id := strings.Repeat("a", 32)
	inv := space.Invite{Base: "https://directory.example", Space: id, Owner: "sha256:" + strings.Repeat("b", 64)}
	client := desktopSpaceMCP(t, n)
	desktopSpaceTool(t, client, "space_join", map[string]any{"link": inv.String()})
	saves := enroll.Saves(filepath.Join(dir, "enrollments"))
	kept := enroll.Saved{Link: inv.String(), Enroll: protocol.SpaceEnroll{Space: id}}
	if err := saves.Save(kept); err != nil {
		t.Fatal(err)
	}
	n.trackSaved(kept, "awaiting_approval")
	args := LeaveArgs{Space: id, Confirm: id}
	if err := n.leaveSpace(context.Background(), args); err == nil {
		t.Fatal("expected refusal")
	}
	if _, ok := saves.Load(id); !ok || len(n.pairbookStatus().Enrollments) != 1 || len(listedSpaces(t, n).Spaces) != 1 {
		t.Fatal("failure discarded local enrollment")
	}
	fixture.revokeErr = nil
	n.joins.life, n.joins.stop = context.WithCancel(context.Background())
	defer n.stopJoins()
	n.following(id)
	lock, err := takeFollow(n.lockPath(id))
	if err != nil {
		t.Fatal(err)
	}
	ctx := n.workerContext(id)
	n.spawn(func() { <-ctx.Done(); n.stopFollowing(id, lock) })
	desktopSpaceTool(t, client, "pairbook_leave", args)
	if _, ok := saves.Load(id); ok || len(n.pairbookStatus().Enrollments) != 0 {
		t.Fatal("left enrollment survived")
	}
	n.resumeJoins()
	if _, already := n.following(id); already {
		t.Fatal("fresh join was claimed by a stale follower")
	}
	n.stopFollowing(id, nil)
}
