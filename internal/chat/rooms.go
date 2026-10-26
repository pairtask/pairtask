package chat

import (
	"errors"
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
	kindRoom       = "room"
	kindRoomDelete = "room delete"
	panelRooms     = "rooms"
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
	case fields[0] == "delete":
		return m.deleteRoom(fields[1])
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

// deleteRoom asks the broker to delete a room; the tab is hidden once it
// confirms.
func (m *Model) deleteRoom(room string) tea.Cmd {
	m.status = "deleting #" + room + "…"
	return m.request(kindRoomDelete, protocol.Message{Type: protocol.TypeOperatorRoom, RoomOp: protocol.RoomOpDelete, Room: room}, room)
}

// roomDeleted installs the rooms and hides the deleted room's tab like
// /close: its messages stay in All, and an unsent draft stays with the
// tab, which /room NAME reopens.
func (m *Model) roomDeleted(req pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeOperatorRoomResult || resp.RoomList == nil {
		m.status = "room delete refused: " + brokerReason(resp)
		return nil
	}
	m.rooms, m.roomsCut = resp.RoomList.Rooms, resp.RoomList.Truncated
	room := req.ids[0]
	key := roomKey(room)
	if m.conv.selected == key {
		m.selectView(allKey)
	}
	m.conv.hidden[key] = true
	m.refreshDirectory()
	m.status = "deleted #" + room + "; its messages stay in All" + m.draftNote(key)
	return nil
}

// draftNote tells that a closed conversation still holds an unsent draft.
func (m *Model) draftNote(key viewKey) string {
	if v := m.conv.saved[key]; v != nil && v.protected() {
		return "; your draft is kept"
	}
	return ""
}

// roomResult installs the rooms and, after a create, opens the new tab.
func (m *Model) roomResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeOperatorRoomResult || resp.RoomList == nil {
		m.status = "room change refused: " + brokerReason(resp)
		return nil
	}
	m.rooms, m.roomsCut = resp.RoomList.Rooms, resp.RoomList.Truncated
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
		if r.MembersTruncated {
			members += fmt.Sprintf(" (+%d more)", r.MembersTotal-len(r.Members))
		}
		rows = append(rows, fmt.Sprintf("  #%s  %s", safe(r.ID), members))
	}
	if m.roomsCut {
		rows = append(rows, "  (room list is partial: more rooms exist than shown)")
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
	if want, ok := roomVerbArgs[fields[0]]; ok {
		return roomVerbArg(fields, want)
	}
	if len(fields) > 1 {
		return errors.New(roomUsage)
	}
	return protocol.ValidateRoomID(fields[0])
}

const roomUsage = "usage: /room NAME, /room add NAME @a, /room remove NAME @a, /room delete NAME"

// roomVerbArgs is how many words each /room verb needs, itself included;
// add and remove take any number of members after the room.
var roomVerbArgs = map[string]int{"add": 3, "remove": 3, "delete": 2}

func roomVerbArg(fields []string, want int) error {
	verb := fields[0]
	switch {
	case verb == "delete" && len(fields) != want:
		return errors.New("usage: /room delete NAME")
	case len(fields) < want:
		return fmt.Errorf("usage: /room %s ROOM @agent …", verb)
	}
	return protocol.ValidateRoomID(fields[1])
}
