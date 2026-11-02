//go:build linux

package server

import (
	"fmt"
	"slices"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func mentionedRoomMessage(id string, ids ...string) map[string]any {
	m := roomOperatorMessage(id, "alpha")
	m["mentions"] = ids
	return m
}

func requireMentions(t *testing.T, frame sessionWire, want ...string) {
	t.Helper()
	got := sessionDecode[[]string](t, frame, "mentions")
	if !slices.Equal(got, want) {
		t.Fatalf("mentions = %q, want %q in %s", got, want, sessionJSON(frame))
	}
}

func TestMentionsAcceptanceRoomAudienceHistorySnapshotAndReplay(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 16)
	b, _ := roomPeer(f, "alpha-two", 9)
	roomPeer(f, "beta-one", 9)
	message := mentionedRoomMessage("addressed-room", "alpha-one", "alpha-one")
	roomRecipients(t, roomOperator(f, message), "alpha-one", "alpha-two")
	for _, peer := range []*sessionProbe{a, b} {
		frame := peer.frame()
		requireMentions(t, frame, "alpha-one")
		operatorEqual(t, "room audience unchanged", frame.text("room"), "alpha")
		operatorEqual(t, "trusted operator origin", frame.text("origin"), "operator")
	}
	var count int
	operatorNoError(t, f.db.QueryRow("SELECT COUNT(*) FROM durable_inbox WHERE message_id='addressed-room' AND acknowledged_at IS NULL").Scan(&count))
	operatorEqual(t, "mentions never auto-ack either recipient", count, 2)
	page := historyRead(f, 0, 0, 16)
	if !slices.Equal(page.Entries[0].Message.Mentions, []string{"alpha-one"}) {
		t.Fatal("history lost mentions")
	}
	observer := sessionOpen(f)
	snapshot, _ := observer.request("operator_snapshot", nil)
	view := sessionDecode[protocol.OperatorSnapshot](t, snapshot, "snapshot")
	found := false
	for _, sent := range view.RecentSent {
		if sent.MessageID == "addressed-room" {
			found = slices.Equal(sent.Mentions, []string{"alpha-one"})
		}
	}
	operatorEqual(t, "snapshot preserves addressed IDs", found, true)
	observer.Close()
	roomDisconnect(f, a, "alpha-one")
	again, _ := roomPeer(f, "alpha-one", 16)
	replay := again.frame()
	requireMentions(t, replay, "alpha-one")
	operatorEqual(t, "durable replay remains replay", sessionDecode[bool](t, replay, "replayed"), true)
}

func TestMentionsAcceptanceGroupIncludesUnaddressedAndLegacyRecipients(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 9)
	b, _ := roomPeer(f, "beta-one", 6)
	m := roomMessage("addressed-group", "")
	m["type"], m["group"], m["mentions"] = "operator_send", []string{"alpha-one", "beta-one"}, []string{"alpha-one"}
	roomRecipients(t, roomOperator(f, m), "alpha-one", "beta-one")
	for _, peer := range []*sessionProbe{a, b} {
		requireMentions(t, peer.frame(), "alpha-one")
	}
}

func TestMentionsAcceptanceRetryIdentitySurvivesMembershipRemovalAndRoomDeletion(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 16)
	b, _ := roomPeer(f, "alpha-two", 16)
	m := mentionedRoomMessage("mentioned-retry", "alpha-two", "alpha-one", "alpha-one")
	roomRecipients(t, roomOperator(f, m), "alpha-one", "alpha-two")
	a.frame()
	b.frame()
	roomChange(f, "remove", "alpha-one", "alpha")
	m["mentions"] = []string{"alpha-one", "alpha-two"}
	retry := roomOperator(f, m)
	operatorEqual(t, "canonical retry after leave accepted", retry.text("stage"), "accepted")
	operatorEqual(t, "same stored request", sessionDecode[bool](t, retry, "repeated"), true)
	roomChange(f, "delete", "", "alpha")
	operatorEqual(t, "same request after room deletion", roomOperator(f, m).text("stage"), "accepted")
	m["mentions"] = []string{"alpha-two"}
	operatorEqual(t, "changed attention conflicts before membership admission", roomOperator(f, m).text("code"), "duplicate_message")
	operatorEqual(t, "retry never duplicates message", f.count("messages"), 1)
}

func TestMentionsAcceptanceRejectsNonmembersWrongScopesAndAgentForgery(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 16)
	for _, tc := range []struct {
		name, code string
		change     func(map[string]any)
	}{
		{"nonmember", "invalid_mention", func(m map[string]any) { m["mentions"] = []string{"beta-one"} }},
		{"invalid-id", "malformed_frame", func(m map[string]any) { m["mentions"] = []string{""} }},
		{"square-offline-tag", "invalid_mention", func(m map[string]any) {
			delete(m, "room")
			m["mentions"] = []string{"beta-one"}
		}},
		{"direct", "malformed_frame", func(m map[string]any) { delete(m, "room"); m["to"] = "alpha-one" }},
		{"group-outsider", "invalid_mention", func(m map[string]any) {
			delete(m, "room")
			delete(m, "to")
			m["group"] = []string{"alpha-one", "alpha-two"}
			m["mentions"] = []string{"beta-one"}
		}},
	} {
		m := mentionedRoomMessage(tc.name, "alpha-one")
		tc.change(m)
		operatorEqual(t, tc.name, roomOperator(f, m).text("code"), tc.code)
	}
	forged := roomScope("forged-attention", "alpha")
	forged["mentions"] = []string{"alpha-one"}
	operatorEqual(t, "agents cannot supply operator attention", roomSend(a, forged).text("code"), "malformed_frame")
	operatorEqual(t, "refused messages never stored", f.count("messages"), 0)
}

func TestMentionsAcceptanceLimitCountsUniqueMembers(t *testing.T) {
	f := roomFixture(t)
	a, _ := roomPeer(f, "alpha-one", 16)
	peers := []*sessionProbe{a}
	var ids []string
	for i := 0; i < 17; i++ {
		id := fmt.Sprintf("member-%02d", i)
		ids = append(ids, id)
		roomChange(f, "add", id, "alpha")
		peer, _ := roomPeer(f, id, 16)
		peers = append(peers, peer)
	}
	audience := append([]string{"alpha-one"}, ids...)
	roomRecipients(t, roomOperator(f, mentionedRoomMessage("sixteen", ids[:16]...)), slices.Clone(audience)...)
	for _, peer := range peers {
		requireMentions(t, peer.frame(), ids[:16]...)
	}
	tooMany := roomOperator(f, mentionedRoomMessage("seventeen", ids...))
	operatorEqual(t, "seventeen unique IDs refused", tooMany.text("type"), "error")
	duplicates := make([]string, 40)
	for i := range duplicates {
		duplicates[i] = "alpha-one"
	}
	roomRecipients(t, roomOperator(f, mentionedRoomMessage("duplicates", duplicates...)), slices.Clone(audience)...)
	requireMentions(t, a.frame(), "alpha-one")
	operatorEqual(t, "only valid messages stored", f.count("messages"), 2)
}
