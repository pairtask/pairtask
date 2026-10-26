package chat

import (
	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

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
