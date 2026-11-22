package hub

import (
	"context"
	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/space"
	"testing"
	"time"
)

func membersFrame(h *Hub, version int) protocol.Message {
	in := Inbound{From: &Client{ProtocolVersion: version}, Msg: protocol.Message{Type: protocol.TypeSpacePeopleGet, RequestID: "members-test"}}
	return workspaceFrames[in.Msg.Type](h, context.Background(), in)
}

func requireFrameWorkspace(t *testing.T, m protocol.Message, workspace string, known bool) {
	t.Helper()
	if m.Type != protocol.TypeSpacePeopleResult || m.RequestID != "members-test" {
		t.Fatalf("unexpected reply: %+v", m)
	}
	r := m.SpaceResult
	if r == nil || r.StatusKnown != known {
		t.Fatalf("unexpected status: %+v", r)
	}
	if len(r.People) != 1 {
		t.Fatalf("visible people: %+v", r.People)
	}
	ws := r.People[0].Workspaces
	if len(ws) != 1 || ws[0].Workspace != workspace {
		t.Fatalf("visible workspaces: %+v", ws)
	}
}

func TestMemberFrameFollowsCurrentMapAndCache(t *testing.T) {
	f := newRetryFixture(t)
	ctx := context.Background()
	now := time.UnixMilli(f.m.IssuedAt)
	clock := realSpaceTiming()
	clock.Now = func() time.Time { return now }
	f.h.SetSpaceTiming(clock)
	messageWorkerOK(t, f.h.store.PinSpace(ctx, f.pin))
	if m := membersFrame(f.h, 32); m.Code != protocol.ErrMalformedFrame {
		t.Fatalf("v32 reply: %+v", m)
	}
	if m := membersFrame(f.h, 33); m.Type != protocol.TypeError {
		t.Fatalf("absent map reply: %+v", m)
	}
	messageWorkerOK(t, f.h.KeepSpaceMap(ctx, f.pin.Space, f.m))
	self := f.binding.Workspace()
	requireFrameWorkspace(t, membersFrame(f.h, 33), self, false)
	cached := []space.PersonStatus{
		{Name: "Owner", Workspaces: []space.WorkspaceStatus{{Workspace: self, Online: true}, {Workspace: "ungranted"}}},
		{Name: "Other", Workspaces: []space.WorkspaceStatus{{Workspace: "other"}}},
	}
	f.h.statusCache.put(f.pin.Space, cached, now)
	m := membersFrame(f.h, 33)
	requireFrameWorkspace(t, m, self, true)
	if !m.SpaceResult.People[0].Workspaces[0].Online {
		t.Fatal("fresh status missing")
	}
	now = now.Add(statusMaxAge + time.Second)
	requireFrameWorkspace(t, membersFrame(f.h, 33), self, false)
	now = time.UnixMilli(f.m.ExpiresAt).Add(time.Second)
	if m := membersFrame(f.h, 33); m.Type != protocol.TypeError {
		t.Fatalf("expired map reply: %+v", m)
	}
}
