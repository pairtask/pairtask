package chat

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Rooms (#153): opt-in spaces for a group's discussion. Each room has a tab;
// a draft there goes to "*" in that room, reaching its members and no one
// else. All keeps everything; the Square is "*" without a room.

const (
	kindRoom   = "room"
	panelRooms = "rooms"
)

// roomKey is a room's conversation.
func roomKey(room string) viewKey { return viewKey{room: room} }

func (k viewKey) isRoom() bool { return k.room != "" }

// roomEntryViews is a "*" message's conversation: its room's tab, or none
// beyond All for the Square.
func roomEntryViews(e *entry) []viewKey {
	if e.msg.Room == "" {
		return nil
	}
	return []viewKey{roomKey(e.msg.Room)}
}

// roomsCommand is /room: open a room's tab (creating the room when new), or
// add or remove members.
func (m *Model) roomsCommand(arg argument) tea.Cmd {
	fields := strings.Fields(string(arg))
	switch {
	case len(fields) == 0:
		m.panel = &panel{kind: panelRooms}
		return nil
	case fields[0] == "add" || fields[0] == "remove":
		return m.roomMembersCommand(fields)
	}
	return m.openRoom(fields[0])
}

// openRoom selects a room's tab, asking the broker to create it first when
// it isn't known yet.
func (m *Model) openRoom(room string) tea.Cmd {
	if m.roomKnown(room) {
		m.refreshDirectory()
		m.selectView(roomKey(room))
		return nil
	}
	m.status = "creating #" + room + "…"
	return m.request(kindRoom, protocol.Message{Type: protocol.TypeOperatorRoom, RoomOp: protocol.RoomOpCreate, Room: room}, room)
}

func (m *Model) roomMembersCommand(fields []string) tea.Cmd {
	if len(fields) < 3 {
		m.status = "usage: /room " + fields[0] + " ROOM @agent …"
		return nil
	}
	op := protocol.RoomOpAdd
	if fields[0] == "remove" {
		op = protocol.RoomOpRemove
	}
	var cmds []tea.Cmd
	for _, id := range fields[2:] {
		cmds = append(cmds, m.request(kindRoom, protocol.Message{Type: protocol.TypeOperatorRoom, RoomOp: op, Room: fields[1], AgentID: strings.TrimPrefix(id, "@")}))
	}
	m.status = fmt.Sprintf("%s %d in #%s…", op, len(fields)-2, fields[1])
	return tea.Batch(cmds...)
}

// roomResult installs the rooms and, after a create, opens the new tab.
func (m *Model) roomResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeOperatorRoomResult || resp.RoomList == nil {
		m.status = "room change refused: " + brokerReason(resp)
		return nil
	}
	m.rooms = resp.RoomList.Rooms
	m.refreshDirectory()
	if len(req.ids) == 1 {
		m.selectView(roomKey(req.ids[0]))
	}
	m.status = "rooms updated"
	return nil
}

func (m *Model) roomKnown(room string) bool {
	return slices.ContainsFunc(m.rooms, func(r protocol.RoomInfo) bool { return r.ID == room })
}

// roomMembers is a room's members from the latest snapshot.
func (m *Model) roomMembers(room string) []string {
	for _, r := range m.rooms {
		if r.ID == room {
			return r.Members
		}
	}
	return nil
}

// roomRows are the /room panel: every room and its members.
func (m *Model) roomRows() []string {
	rows := []string{titleStyle.Render("Rooms") + mutedStyle.Render("  (Esc closes)")}
	if len(m.rooms) == 0 {
		rows = append(rows, "  no rooms yet; /room NAME creates one and opens its tab")
	}
	for _, r := range m.rooms {
		members := "no members"
		if len(r.Members) > 0 {
			members = safe(strings.Join(r.Members, ", "))
		}
		rows = append(rows, fmt.Sprintf("  #%s  %s", safe(r.ID), members))
	}
	return append(rows, mutedStyle.Render("  /room NAME opens a room · /room add NAME @a @b · /room remove NAME @a"))
}

// roomArg checks /room: nothing, a room name, or add/remove with a room and
// agents.
func roomArg(a argument) error {
	fields := strings.Fields(string(a))
	if len(fields) == 0 {
		return nil
	}
	name := fields[0]
	if name == "add" || name == "remove" {
		if len(fields) < 3 {
			return fmt.Errorf("usage: /room %s ROOM @agent …", name)
		}
		name = fields[1]
	} else if len(fields) > 1 {
		return fmt.Errorf("usage: /room NAME, /room add NAME @a, /room remove NAME @a")
	}
	return protocol.ValidateRoomID(name)
}
