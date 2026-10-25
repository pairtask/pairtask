//go:build linux

package server

import "testing"

func TestRoomsAcceptanceAcceptedRetrySurvivesLeaveWithoutRerouting(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 9)
	b, _ := roomPeer(f, "alpha-two", 9)
	m := roomScope("accepted-before-leave", "alpha")
	roomRecipients(t, roomSend(a, m), "alpha-two", "operator")
	b.frame()
	roomChange(f, "remove", "alpha-one", "alpha")
	roomChange(f, "add", "beta-one", "alpha")
	roomPeer(f, "beta-one", 9)
	retry := roomSend(a, m)
	roomRecipients(t, retry, "alpha-two", "operator")
	operatorEqual(t, "old outcome reported", sessionDecode[bool](t, retry, "repeated"), true)
	operatorEqual(t, "no new stored route", f.count("messages"), 1)
	m["message_id"] = "new-after-leave"
	operatorEqual(t, "new message still requires membership", roomSend(a, m).text("code"), "room_denied")
}

func TestRoomsAcceptanceOperatorReplyInheritsParentRoom(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 9)
	b, _ := roomPeer(f, "alpha-two", 9)
	roomPeer(f, "beta-one", 9)
	roomRecipients(t, roomSend(a, roomScope("agent-parent", "alpha")), "alpha-two", "operator")
	b.frame()
	reply := roomOperatorMessage("operator-reply", "")
	delete(reply, "room")
	reply["in_reply_to"] = "agent-parent"
	roomRecipients(t, roomOperator(f, reply), "alpha-one", "alpha-two")
	operatorEqual(t, "operator reply retains room", a.frame().text("room"), "alpha")
	reply["message_id"], reply["room"] = "operator-mismatch", "square"
	operatorEqual(t, "operator explicit mismatch", roomOperator(f, reply).text("code"), "invalid_reply")
	operatorEqual(t, "mismatch never persists", f.count("messages"), 2)
}
