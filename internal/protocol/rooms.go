package protocol

import (
	"errors"
	"regexp"
)

// Project rooms (#153, v9): opt-in spaces for a group's discussion. The
// Square is everyone: a "*" without a room reaches every agent, as before. A
// "*" to a room reaches that room's members and the operator. Agents may be in
// several rooms; direct messages work between anyone. Rooms organise talk;
// they are not a security boundary.
const (
	TypeRoomJoin           = "room_join"            // agent → broker: join a room, creating it if new
	TypeRoomLeave          = "room_leave"           // agent → broker: leave a room
	TypeRoomList           = "room_list"            // agent → broker: its rooms, read from the broker
	TypeRoomResult         = "room_result"          // broker → agent: its rooms afterwards
	TypeOperatorRoom       = "operator_room"        // operator → broker: list, create, add, remove or delete
	TypeOperatorRoomResult = "operator_room_result" // broker → operator: the rooms afterwards

	RoomSquare = "square" // everyone; the same as no room

	EventRoomsChanged = "rooms_changed" // operator session: a room or its members changed

	RoomOpList   = "list"
	RoomOpCreate = "create"
	RoomOpAdd    = "add"
	RoomOpRemove = "remove"
	RoomOpDelete = "delete" // operator only: the room and its memberships go; messages keep their room (#164)

	ErrRoomDenied     = "room_denied"     // posting to a room the agent isn't in
	ErrInvalidRoom    = "invalid_room"    // malformed, reserved or unknown room
	ErrInvalidMention = "invalid_mention" // a mention outside the room or group, or too many
)

// MaxMentions bounds the members one operator message addresses.
const MaxMentions = 16

// Bounds on a room listing: rooms in ID order, members in ID order per room.
// Totals and truncation flags keep every omission visible.
const (
	ListedRooms       = 64
	ListedRoomMembers = 64
)

// RoomList is the rooms and their members, bounded.
type RoomList struct {
	Rooms     []RoomInfo `json:"rooms"`
	Total     int        `json:"rooms_total"`
	Truncated bool       `json:"rooms_truncated"`
}

// RoomInfo is one room and its members, sorted and bounded.
type RoomInfo struct {
	ID               string   `json:"id"`
	Members          []string `json:"members"`
	MembersTotal     int      `json:"members_total"`
	MembersTruncated bool     `json:"members_truncated"`
}

var roomID = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// ValidateRoomID checks a room name; square is the everyone scope, not a room.
func ValidateRoomID(id string) error {
	if !roomID.MatchString(id) {
		return errors.New("room must be 1 to 64 of a-z, 0-9 and -")
	}
	if id == RoomSquare {
		return errors.New("square is everyone, not a room")
	}
	return nil
}

// RoomScope is a "*" message's room: "" for the Square.
func RoomScope(room string) string {
	if room == RoomSquare {
		return ""
	}
	return room
}

// RoomFlags collects a repeatable --room command-line flag.
type RoomFlags []string

func (r *RoomFlags) String() string { return "" }

// Set adds one valid room.
func (r *RoomFlags) Set(room string) error {
	if err := ValidateRoomID(room); err != nil {
		return err
	}
	*r = append(*r, room)
	return nil
}
