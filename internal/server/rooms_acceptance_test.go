//go:build linux

package server

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func roomOperator(f *operatorFixture, fields map[string]any) sessionWire {
	f.t.Helper()
	p := &sessionProbe{operatorPeer: operatorDial(f.t, f.operator)}
	defer p.Close()
	p.send(fields)
	return p.frame()
}

func roomCommand(f *operatorFixture, op, agent, room string) sessionWire {
	f.t.Helper()
	return roomOperator(f, map[string]any{"type": "operator_room", "room_op": op, "agent_id": agent, "room": room})
}

func roomChange(f *operatorFixture, op, agent, room string) {
	f.t.Helper()
	r := roomCommand(f, op, agent, room)
	operatorEqual(f.t, "room operation: "+sessionJSON(r), r.text("type"), "operator_room_result")
}

func roomFixture(t *testing.T) *operatorFixture {
	t.Helper()
	f := newOperatorFixture(t, os.Getuid(), "")
	for _, room := range []string{"alpha", "beta"} {
		roomChange(f, "create", "", room)
		for _, id := range []string{room + "-one", room + "-two", room + "-offline"} {
			roomChange(f, "add", id, room)
		}
	}
	return f
}

func roomPeer(f *operatorFixture, id string, version int, rooms ...string) (*sessionProbe, sessionWire) {
	f.t.Helper()
	p := &sessionProbe{operatorPeer: operatorDial(f.t, f.agent)}
	p.send(map[string]any{"type": "hello", "agent_id": id, "protocol_version": version, "rooms": rooms})
	welcome := p.frame()
	operatorEqual(f.t, "room peer registered", welcome.text("type"), "welcome")
	return p, welcome
}

func roomMessage(id, to string) map[string]any {
	return map[string]any{"type": "msg", "message_id": id, "to": to, "durable": true, "ack_requested": true, "payload": map[string]string{"text": "room acceptance"}}
}

func roomSend(p *sessionProbe, message map[string]any) sessionWire {
	p.t.Helper()
	p.send(message)
	return p.frame()
}

func roomRecipients(t *testing.T, frame sessionWire, want ...string) {
	t.Helper()
	operatorEqual(t, "accepted route: "+sessionJSON(frame), frame.text("stage"), "accepted")
	var got []string
	for _, recipient := range sessionDecode[[]protocol.Recipient](t, frame, "recipients") {
		got = append(got, recipient.AgentID)
	}
	slices.Sort(got)
	slices.Sort(want)
	operatorEqual(t, "frozen room recipients", strings.Join(got, ","), strings.Join(want, ","))
}

func roomScope(id, room string) map[string]any {
	m := roomMessage(id, "*")
	m["room"] = room
	return m
}

func roomMembership(t *testing.T, frame sessionWire, want ...string) {
	t.Helper()
	var got []string
	if len(frame["rooms"]) > 0 {
		got = sessionDecode[[]string](t, frame, "rooms")
	}
	slices.Sort(got)
	slices.Sort(want)
	operatorEqual(t, "memberships", strings.Join(got, ","), strings.Join(want, ","))
}

func TestRoomsAcceptanceHelloAddsMembershipAndLiveChangesAreVisible(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	roomChange(f, "add", "worker", "alpha")
	p, welcome := roomPeer(f, "worker", 9, "beta", "beta")
	roomMembership(t, welcome, "alpha", "beta")
	roomChange(f, "add", "worker", "gamma")
	listed, _ := p.request("room_list", nil)
	operatorEqual(t, "list response", listed.text("type"), "room_result")
	roomMembership(t, listed, "alpha", "beta", "gamma")
	left, _ := p.request("room_leave", map[string]any{"room": "alpha"})
	roomMembership(t, left, "beta", "gamma")
	roomDisconnect(f, p, "worker")
	_, welcome = roomPeer(f, "worker", 9)
	roomMembership(t, welcome, "beta", "gamma")
}

func TestRoomsAcceptanceBroadcastStaysWithinProject(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 9)
	b, _ := roomPeer(f, "alpha-two", 9)
	roomPeer(f, "beta-one", 9)
	roomPeer(f, "alpha-offline", 8)
	roomRecipients(t, roomSend(a, roomScope("alpha-public", "alpha")), "alpha-two", "operator")
	delivery := b.frame()
	operatorEqual(t, "delivery scope", delivery.text("room"), "alpha")
	operatorEqual(t, "delivery provenance", delivery.text("origin"), "agent")
	var room string
	operatorNoError(t, f.db.QueryRow("SELECT room FROM message_metadata WHERE message_id='alpha-public'").Scan(&room))
	operatorEqual(t, "stored scope", room, "alpha")
}

func TestRoomsAcceptanceSquareAndLegacyBroadcastsReachEveryone(t *testing.T) {
	for _, version := range []int{6, 8, 9} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			f := roomFixture(t)
			a, _ := roomPeer(f, "alpha-one", version)
			b, _ := roomPeer(f, "beta-one", 9)
			roomPeer(f, "new-agent", 8)
			want := []string{"beta-one", "new-agent"}
			if version >= 7 {
				want = append(want, "operator")
			}
			roomRecipients(t, roomSend(a, roomMessage("square", "*")), want...)
			operatorEqual(t, "Square scope", b.frame().text("room"), "")
		})
	}
}

func TestRoomsAcceptanceSquareAliasAndRetryAreCanonical(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 9)
	b, _ := roomPeer(f, "beta-one", 8)
	m := roomScope("square-alias", "square")
	roomRecipients(t, roomSend(a, m), "beta-one", "operator")
	operatorEqual(t, "Square alias delivered without project scope", b.frame().text("room"), "")
	delete(m, "room")
	retry := roomSend(a, m)
	operatorEqual(t, "same Square retry", sessionDecode[bool](t, retry, "repeated"), true)
	operatorEqual(t, "one stored message", f.count("messages"), 1)
}

func TestRoomsAcceptanceNonmemberPostIsAtomicAndJoinCreatesRoom(t *testing.T) {
	f := roomFixture(t)
	p, _ := roomPeer(f, "new-agent", 9)
	operatorEqual(t, "nonmember refused", roomSend(p, roomScope("refused", "alpha")).text("code"), "room_denied")
	operatorEqual(t, "no history", f.count("messages"), 0)
	operatorEqual(t, "no inbox", f.count("durable_inbox"), 0)
	joined, _ := p.request("room_join", map[string]any{"room": "new-project"})
	operatorEqual(t, "joined result", joined.text("type"), "room_result")
	roomMembership(t, joined, "new-project")
	roomRecipients(t, roomSend(p, roomScope("joined", "new-project")), "operator")
}

func TestRoomsAcceptanceInvalidJoinDoesNotCreateMembership(t *testing.T) {
	for _, room := range []string{"square", "Upper", "bad/name", strings.Repeat("a", 65), ""} {
		t.Run(room, func(t *testing.T) {
			f := newOperatorFixture(t, os.Getuid(), "")
			p, _ := roomPeer(f, "worker", 9)
			result := roomSend(p, map[string]any{"type": "room_join", "request_id": "invalid-join", "room": room})
			operatorEqual(t, "invalid room", result.text("code"), "invalid_room")
			operatorEqual(t, "no room", f.count("rooms"), 0)
			operatorEqual(t, "no membership", f.count("room_members"), 0)
		})
	}
}

func TestRoomsAcceptanceCrossRoomDirectGroupAndStatusRemainAvailable(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 9)
	b, _ := roomPeer(f, "beta-one", 9)
	roomRecipients(t, roomSend(a, roomMessage("direct", "beta-one")), "beta-one")
	operatorEqual(t, "direct delivered", b.frame().text("message_id"), "direct")
	operatorEqual(t, "status declared", statusDeclare(t, b, "active").text("type"), "agent_status_result")
	status, _ := a.request("agent_status_get", map[string]any{"agent_id": "beta-one"})
	operatorEqual(t, "cross-room read", status.text("type"), "agent_status_result")
	group := map[string]any{"type": "operator_send", "message_id": "group", "group": []string{"alpha-one", "beta-one"}, "payload": map[string]string{"text": "cross-room group"}}
	roomRecipients(t, roomOperator(f, group), "alpha-one", "beta-one")
}

func TestRoomsAcceptanceOperatorScopeAndLegacyExclusion(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 9)
	roomPeer(f, "alpha-two", 8)
	roomPeer(f, "beta-one", 9)
	result := roomOperator(f, roomOperatorMessage("project", "alpha"))
	roomRecipients(t, result, "alpha-one")
	excluded := sessionDecode[[]string](t, result, "excluded")
	operatorEqual(t, "legacy exclusion", strings.Join(excluded, ","), "alpha-two")
	operatorEqual(t, "scope delivered", a.frame().text("room"), "alpha")
	roomRecipients(t, roomOperator(f, roomOperatorMessage("square", "")), "alpha-one", "alpha-two", "beta-one")
	operatorEqual(t, "unknown room refused", roomOperator(f, roomOperatorMessage("unknown", "missing")).text("code"), "invalid_room")
	operatorEqual(t, "refusal not stored", f.count("messages"), 2)
}

func TestRoomsAcceptanceBoundedDirectoryKeepsTotalsAndEmptyRooms(t *testing.T) {
	f := roomFixture(t)
	for i := 0; i < 65; i++ {
		_, err := f.db.Exec("INSERT INTO rooms(id,created_at) VALUES(?,?)", fmt.Sprintf("room-%02d", i), "2026-10-25")
		operatorNoError(t, err)
	}
	for i := 0; i < 65; i++ {
		_, err := f.db.Exec("INSERT INTO room_members(room_id,agent_id,joined_at) VALUES('alpha',?,?)", fmt.Sprintf("member-%02d", i), "2026-10-25")
		operatorNoError(t, err)
	}
	result := roomCommand(f, "list", "", "")
	type info struct {
		ID        string   `json:"id"`
		Members   []string `json:"members"`
		Total     int      `json:"members_total"`
		Truncated bool     `json:"members_truncated"`
	}
	type listing struct {
		Rooms     []info `json:"rooms"`
		Total     int    `json:"rooms_total"`
		Truncated bool   `json:"rooms_truncated"`
	}
	got := sessionDecode[listing](t, result, "room_list")
	operatorEqual(t, "directory count", len(got.Rooms), 64)
	operatorEqual(t, "directory total", got.Total, 67)
	operatorEqual(t, "directory omission explicit", got.Truncated, true)
	operatorEqual(t, "member count", len(got.Rooms[0].Members), 64)
	operatorEqual(t, "member total", got.Rooms[0].Total, 68)
	operatorEqual(t, "member omission explicit", got.Rooms[0].Truncated, true)
	operatorEqual(t, "empty room retained", got.Rooms[2].ID, "room-00")
	operatorEqual(t, "empty room has zero members", len(got.Rooms[2].Members), 0)
	// Listing truncation must not truncate actual routing membership.
	a, _ := roomPeer(f, "alpha-one", 9)
	roomPeer(f, "member-64", 9)
	roomRecipients(t, roomSend(a, roomScope("past-list", "alpha")), "member-64", "operator")
}
