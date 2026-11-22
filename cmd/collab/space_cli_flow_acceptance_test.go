//go:build linux

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/space"
	"github.com/pairtask/pairtask/pairbook"
)

func TestSpaceCLIApprovalConnectsTwoWorkspaces(t *testing.T) {
	d := &flowDirectory{addresses: map[string]pairbook.SignedAddress{}}
	owner, member := startFlowNode(t, d), startFlowNode(t, d)
	// The lower fingerprint initiates. Refresh it after the approving hub has kept v2.
	if owner.id.Fingerprint() < member.id.Fingerprint() {
		owner, member = member, owner
	}
	owner.command(t, "create", "Shared office", "--pairbook", "https://directory.example")
	var link inviteLink
	offlineOK(t, json.Unmarshal([]byte(owner.command(t, "invite")), &link))
	inv, err := space.ParseInvite(link.Link)
	offlineOK(t, err)
	for _, n := range []*flowNode{owner, member} {
		offlineOK(t, d.addAddress(inv.Space, n.id, n.address))
	}
	member.command(t, "join", link.Link)
	var requests []string
	offlineOK(t, json.Unmarshal([]byte(owner.command(t, "requests")), &requests))
	if len(requests) != 1 {
		t.Fatalf("got %d requests, want one", len(requests))
	}
	var report foldReport
	offlineOK(t, json.Unmarshal([]byte(owner.command(t, "approve", strings.Fields(requests[0])[0])), &report))
	if report.Applied != 1 {
		t.Fatalf("approval report: %+v", report)
	}
	offlineOK(t, member.h.RefreshSpace(context.Background(), inv.Space))
	var members memberList
	offlineOK(t, json.Unmarshal([]byte(member.command(t, "members")), &members))
	if len(members) != 2 {
		t.Fatalf("got %d people after refresh", len(members))
	}
	for _, side := range [][2]*flowNode{{owner, member}, {member, owner}} {
		awaitFlowContact(t, side[0], side[1])
	}
}
func awaitFlowContact(t *testing.T, local, remote *flowNode) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok, err := local.st.PeerMessages().Confirmed(context.Background(), remote.id.Fingerprint())
		offlineOK(t, err)
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("workspaces did not connect")
}

func TestSpaceCLISyncRefetchesWinningOwnerMap(t *testing.T) {
	d := &flowDirectory{addresses: map[string]pairbook.SignedAddress{}}
	owner := startFlowNode(t, d)
	owner.command(t, "create", "Office", "--pairbook", "https://directory.example")
	d.mu.Lock()
	due := d.current.SpaceMap
	now := time.Now()
	due.Version++
	due.IssuedAt = now.UnixMilli()
	due.ExpiresAt = now.Add(2 * time.Second).UnixMilli()
	d.current = pairbook.SignSpaceMap(owner.keys.PersonKey(), due)
	initial := d.current
	next := due
	next.Version++
	next.IssuedAt = now.UnixMilli()
	next.ExpiresAt = now.Add(time.Hour).UnixMilli()
	winning := pairbook.SignSpaceMap(owner.keys.PersonKey(), next)
	d.winner = &winning
	d.mu.Unlock()
	ctx := context.Background()
	offlineOK(t, owner.h.KeepSpaceMap(ctx, due.Space, initial))
	time.Sleep(time.Until(now.Add(time.Second))) // the current map is now due for renewal
	owner.command(t, "sync")
	_, mark, ok, err := owner.st.SpaceMap(ctx, due.Space)
	offlineOK(t, err)
	if !ok || mark != winning.Mark() {
		t.Fatal("sync did not keep the winner")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conflicts != 1 || d.current.Mark() != winning.Mark() {
		t.Fatal("sync replaced the competing writer's map")
	}
}
