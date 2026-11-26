//go:build linux

package server

import (
	"fmt"
	"testing"

	"github.com/pairtask/pairtask/internal/store"
)

// Both stalled agents remain registered and read frames, but never acknowledge them.
func TestRoomReplySurvivesTwoFullAgentInboxes(t *testing.T) {
	f := roomFixture(t)
	author, _ := roomPeer(f, "alpha-one", 9)
	stalled, _ := roomPeer(f, "alpha-two", 9)
	second, _ := roomPeer(f, "alpha-offline", 9)
	healthy, _ := roomPeer(f, "healthy", 9, "alpha")
	filler, _ := roomPeer(f, "beta-one", 9)
	roomRecipients(t, roomOperator(f, roomOperatorMessage("parent", "alpha")), "alpha-one", "alpha-two", "alpha-offline", "healthy")
	for _, p := range []*sessionProbe{author, stalled, second, healthy} {
		p.frame()
	}
	for i := 1; i < store.MaxPendingMessages; i++ {
		for id, p := range map[string]*sessionProbe{"alpha-two": stalled, "alpha-offline": second} {
			roomRecipients(t, roomSend(filler, roomMessage(fmt.Sprintf("fill-%s-%d", id, i), id)), id)
			p.frame()
		}
	}
	msg := roomScope("public-reply", "alpha")
	msg["in_reply_to"] = "parent"
	first := roomSend(author, msg)
	checkRoomCapacityOutcome(t, first)
	operatorEqual(t, "healthy recipient got reply", healthy.frame().text("message_id"), "public-reply")
	roomAcknowledge(stalled, "parent")
	retry := roomSend(author, msg)
	checkRoomCapacityOutcome(t, retry)
	operatorEqual(t, "retry retains sequence", string(retry["seq"]), string(first["seq"]))
	operatorEqual(t, "retry reported", sessionDecode[bool](t, retry, "repeated"), true)
	page := inboxReadPage(f, 0, 0, 32)
	operatorEqual(t, "exactly one operator copy", len(page.Messages), 1)
	operatorEqual(t, "operator received reply", page.Messages[0].MessageID, "public-reply")
	// A new post can reach the recovered peer. An old reply is never redelivered.
	next := roomSend(author, roomScope("after-recovery", "alpha"))
	roomRecipients(t, next, "alpha-two", "healthy", "operator")
	operatorEqual(t, "recovered peer receives new post", stalled.frame().text("message_id"), "after-recovery")
}

func checkRoomCapacityOutcome(t *testing.T, frame sessionWire) {
	t.Helper()
	roomRecipients(t, frame, "healthy", "operator")
	excluded := sessionDecode[[]string](t, frame, "excluded")
	operatorEqual(t, "full agents reported", fmt.Sprint(excluded), "[alpha-offline alpha-two]")
}
