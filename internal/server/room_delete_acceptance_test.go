//go:build linux

package server

import (
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestRoomDeleteAcceptanceRemovesMembershipAuditsAndPreservesPendingReplay(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 9)
	roomRecipients(t, roomOperator(f, roomOperatorMessage("pending-before-delete", "alpha")), "alpha-one")
	a.frame()
	spec := claimSpec{parent: "pending-before-delete", task: "reply", role: "owner"}
	operatorEqual(t, "claim before delete", claimCall(a, "claim_set", spec).text("type"), "claim_result")
	roomChange(f, "delete", "", "alpha")
	listed, _ := a.request("room_list", nil)
	roomMembership(t, listed)
	operatorEqual(t, "accepted claim remains", claimCall(a, "claim_get", spec).text("type"), "claim_result")
	var count int
	operatorNoError(t, f.db.QueryRow("SELECT COUNT(*) FROM operator_audit WHERE action='room delete' AND scope='alpha' AND result='accepted'").Scan(&count))
	operatorEqual(t, "successful deletion audited", count, 1)
	operatorNoError(t, f.db.QueryRow("SELECT COUNT(*) FROM room_members WHERE room_id='alpha'").Scan(&count))
	operatorEqual(t, "all membership removed", count, 0)
	operatorNoError(t, f.db.QueryRow("SELECT COUNT(*) FROM room_members WHERE room_id='beta'").Scan(&count))
	operatorEqual(t, "other room unchanged", count, 3)
	roomDisconnect(f, a, "alpha-one")
	again, welcome := roomPeer(f, "alpha-one", 9)
	roomMembership(t, welcome)
	replay := again.frame()
	operatorEqual(t, "accepted message replays after deletion", replay.text("message_id"), "pending-before-delete")
	operatorEqual(t, "history room preserved", replay.text("room"), "alpha")
	operatorEqual(t, "fresh agent post denied", roomSend(again, roomScope("fresh-after-delete", "alpha")).text("code"), "room_denied")
	operatorEqual(t, "fresh operator post denied", roomOperator(f, roomOperatorMessage("fresh-operator", "alpha")).text("code"), "invalid_room")
}

func TestRoomDeleteAcceptanceAcceptedRetriesKeepOriginalOutcome(t *testing.T) {
	for _, operator := range []bool{false, true} {
		name := "agent"
		if operator {
			name = "operator"
		}
		t.Run(name, func(t *testing.T) {
			f := roomFixture(t)
			a, _ := roomPeer(f, "alpha-one", 9)
			b, _ := roomPeer(f, "alpha-two", 9)
			message := roomScope("accepted-before-delete", "alpha")
			send := func() sessionWire { return roomSend(a, message) }
			if operator {
				message["type"] = "operator_send"
				send = func() sessionWire { return roomOperator(f, message) }
			}
			operatorEqual(t, "initial accepted", send().text("stage"), "accepted")
			b.frame()
			if operator {
				a.frame()
			}
			roomChange(f, "delete", "", "alpha")
			retry := send()
			operatorEqual(t, "same accepted outcome after delete: "+sessionJSON(retry), retry.text("stage"), "accepted")
			operatorEqual(t, "reported as retry", sessionDecode[bool](t, retry, "repeated"), true)
			operatorEqual(t, "no reroute or extra message", f.count("messages"), 1)
		})
	}
}

func TestRoomDeleteAcceptanceUnknownAgentAndMalformedRequestsCannotDelete(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 9)
	denied := roomSend(a, map[string]any{"type": "operator_room", "room_op": "delete", "room": "alpha"})
	operatorEqual(t, "agent cannot delete", denied.text("type"), "error")
	operatorEqual(t, "room still exists", f.count("rooms"), 2)
	operatorEqual(t, "unknown room explicit error", roomCommand(f, "delete", "", "unknown").text("code"), "invalid_room")
	operatorEqual(t, "delete rejects member argument", roomCommand(f, "delete", "alpha-one", "alpha").text("code"), "malformed_frame")
	operatorEqual(t, "Square cannot be deleted", roomCommand(f, "delete", "", "square").text("type"), "error")
	operatorEqual(t, "bad requests leave rooms", f.count("rooms"), 2)
}

func TestRoomDeleteAcceptanceAuditFailurePreventsDeletion(t *testing.T) {
	f := roomFixture(t)
	inboxExec(f, `CREATE TRIGGER fail_room_audit BEFORE INSERT ON operator_audit WHEN NEW.action='room delete' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	operatorEqual(t, "audit failure visible", roomCommand(f, "delete", "", "alpha").text("code"), protocol.ErrInternal)
	operatorEqual(t, "rooms retained", f.count("rooms"), 2)
	operatorEqual(t, "members retained", f.count("room_members"), 6)
}

func TestRoomDeleteAcceptanceLiveOperatorNotifiedAndHelloCanRecreate(t *testing.T) {
	f := roomFixture(t)
	observer := sessionOpen(f)
	observer.request("operator_snapshot", nil)
	roomChange(f, "delete", "", "alpha")
	found := false
	for i := 0; i < 8 && !found; i++ {
		frame := observer.frame()
		if frame.text("type") == "event" {
			found = sessionDecode[protocol.SessionEvent](t, frame, "event").Kind == "rooms_changed"
		}
	}
	operatorEqual(t, "live directory notified", found, true)
	observer.Close()
	_, welcome := roomPeer(f, "replacement", 9, "alpha")
	roomMembership(t, welcome, "alpha")
	var count int
	operatorNoError(t, f.db.QueryRow("SELECT COUNT(*) FROM room_members WHERE room_id='alpha'").Scan(&count))
	operatorEqual(t, "hello recreates fresh membership only", count, 1)
}
