//go:build linux

package server

import (
	"github.com/pairtask/pairtask/internal/protocol"
	"os"
	"testing"
)

func TestRoomsAcceptanceHelloMembershipNotifiesOpenOperatorChat(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	operator := sessionOpen(f)
	operator.request("operator_snapshot", nil)
	roomPeer(f, "worker", 9, "fresh-room")
	snapshot, events := operator.request("operator_snapshot", nil)
	found := false
	for _, frame := range events {
		if frame.text("type") != "event" {
			continue
		}
		event := sessionDecode[protocol.SessionEvent](t, frame, "event")
		if event.Kind == "rooms_changed" {
			found = true
		}
	}
	operatorEqual(t, "hello updates live room directory", found, true)
	type room struct {
		ID      string   `json:"id"`
		Members []string `json:"members"`
	}
	result := sessionDecode[struct {
		Rooms []room `json:"rooms"`
	}](t, snapshot, "snapshot")
	operatorEqual(t, "new room in snapshot", len(result.Rooms), 1)
	operatorEqual(t, "new room name", result.Rooms[0].ID, "fresh-room")
	operatorEqual(t, "joining member", len(result.Rooms[0].Members), 1)
	operatorEqual(t, "joining member identity", result.Rooms[0].Members[0], "worker")
}
