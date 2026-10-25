//go:build linux

package server

import (
	"github.com/pairtask/pairtask/internal/protocol"
	"testing"
	"time"
)

func roomDisconnect(f *operatorFixture, p *sessionProbe, id string) {
	f.t.Helper()
	observer := sessionAcquireAfterClose(f)
	defer observer.Close()
	operatorNoError(f.t, p.Close())
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, _ := observer.request("operator_snapshot", nil)
		snapshot := sessionDecode[struct {
			Agents []protocol.AgentPresence `json:"agents"`
		}](f.t, response, "snapshot")
		connected := false
		for _, agent := range snapshot.Agents {
			if agent.AgentID == id {
				connected = true
			}
		}
		if !connected {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatal("room peer did not disconnect")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func roomAcknowledge(p *sessionProbe, id string) {
	p.t.Helper()
	for _, stage := range []string{"adapter_received", "agent_acknowledged"} {
		p.send(map[string]any{"type": "ack", "message_id": id, "stage": stage})
	}
	operatorEqual(p.t, "explicit acknowledgment confirmed", p.frame().text("stage"), "agent_acknowledged")
}

func roomOperatorMessage(id, room string) map[string]any {
	m := roomMessage(id, "*")
	m["type"], m["room"] = "operator_send", room
	return m
}

func TestRoomsAcceptanceReplyInheritsRoomAndExplicitSquareIsMismatch(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 9)
	b, _ := roomPeer(f, "alpha-two", 9)
	roomPeer(f, "beta-one", 9)
	roomRecipients(t, roomSend(a, roomScope("parent", "alpha")), "alpha-two", "operator")
	b.frame()
	reply := roomMessage("reply", "*")
	reply["in_reply_to"] = "parent"
	roomRecipients(t, roomSend(b, reply), "alpha-one", "operator")
	operatorEqual(t, "inherited room", a.frame().text("room"), "alpha")
	reply["message_id"], reply["room"] = "mismatch", "square"
	operatorEqual(t, "explicit Square mismatch", roomSend(b, reply).text("code"), "invalid_reply")
	operatorEqual(t, "mismatch not stored", f.count("messages"), 2)
}

func TestRoomsAcceptanceRetriesCannotChangeRoom(t *testing.T) {
	for _, operator := range []bool{false, true} {
		name := "agent"
		if operator {
			name = "operator"
		}
		t.Run(name, func(t *testing.T) {
			f := roomFixture(t)
			a, _ := roomPeer(f, "alpha-one", 9, "beta")
			b, _ := roomPeer(f, "alpha-two", 9)
			m := roomScope("same-id", "alpha")
			send := func() sessionWire { return roomSend(a, m) }
			if operator {
				m["type"] = "operator_send"
				send = func() sessionWire { return roomOperator(f, m) }
			}
			operatorEqual(t, "first accepted", send().text("stage"), "accepted")
			b.frame()
			retry := send()
			operatorEqual(t, "identical retry", sessionDecode[bool](t, retry, "repeated"), true)
			m["room"] = "beta"
			operatorEqual(t, "changed room", send().text("code"), "duplicate_message")
			operatorEqual(t, "no reroute", f.count("messages"), 1)
		})
	}
}

func TestRoomsAcceptanceLeaveKeepsPendingDeliveryAndClaims(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 9)
	roomRecipients(t, roomOperator(f, roomOperatorMessage("pending", "alpha")), "alpha-one")
	a.frame()
	spec := claimSpec{parent: "pending", task: "reply", role: "owner"}
	operatorEqual(t, "claim accepted", claimCall(a, "claim_set", spec).text("type"), "claim_result")
	roomChange(f, "remove", "alpha-one", "alpha")
	rooms, _ := a.request("room_list", nil)
	roomMembership(t, rooms)
	operatorEqual(t, "existing claim accessible", claimCall(a, "claim_get", spec).text("type"), "claim_result")
	roomDisconnect(f, a, "alpha-one")
	again, welcome := roomPeer(f, "alpha-one", 9)
	roomMembership(t, welcome)
	replay := again.frame()
	operatorEqual(t, "accepted delivery survives leave", replay.text("message_id"), "pending")
	operatorEqual(t, "room retained", replay.text("room"), "alpha")
	operatorEqual(t, "replayed", sessionDecode[bool](t, replay, "replayed"), true)
	operatorEqual(t, "new post denied", roomSend(again, roomScope("after-leave", "alpha")).text("code"), "room_denied")
}

func TestRoomsAcceptanceDowngradeDefersRoomReplayWithoutClaimingIt(t *testing.T) {
	for _, source := range []string{"agent", "operator"} {
		t.Run(source, func(t *testing.T) {
			f := roomFixture(t)
			a, _ := roomPeer(f, "alpha-one", 9)
			b, welcome := roomPeer(f, "alpha-two", 9)
			oldSession := welcome.text("session_id")
			if source == "agent" {
				roomSend(a, roomScope("pending", "alpha"))
			} else {
				roomOperator(f, roomOperatorMessage("pending", "alpha"))
				a.frame()
			}
			b.frame()
			roomDisconnect(f, b, "alpha-two")
			legacy, _ := roomPeer(f, "alpha-two", 8)
			// The correlated status response is a barrier: no room frame may precede it.
			result, prior := legacy.request("agent_status_get", map[string]any{"agent_id": "alpha-two"})
			operatorEqual(t, "no room frame before status barrier", len(prior), 0)
			operatorEqual(t, "no replay before response", result.text("type"), "agent_status_result")
			var owner string
			operatorNoError(t, f.db.QueryRow("SELECT session_id FROM durable_inbox WHERE message_id='pending' AND agent_id='alpha-two'").Scan(&owner))
			operatorEqual(t, "legacy cannot reclaim room delivery", owner, oldSession)
			roomDisconnect(f, legacy, "alpha-two")
			current, _ := roomPeer(f, "alpha-two", 9)
			replay := current.frame()
			operatorEqual(t, "capable reconnect receives pending", replay.text("message_id"), "pending")
			operatorEqual(t, "replay scope", replay.text("room"), "alpha")
		})
	}
}

func TestRoomsAcceptanceNonrecipientCannotReadPrivateClaims(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 9)
	b, _ := roomPeer(f, "beta-one", 9)
	parent := roomMessage("private", "alpha-one")
	parent["type"] = "operator_send"
	roomRecipients(t, roomOperator(f, parent), "alpha-one")
	a.frame()
	spec := claimSpec{parent: "private", task: "reply", role: "owner"}
	operatorEqual(t, "recipient claim", claimCall(a, "claim_set", spec).text("type"), "claim_result")
	operatorEqual(t, "nonrecipient denied", claimCall(b, "claim_get", spec).text("type"), "error")
}
