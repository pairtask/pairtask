//go:build linux

package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func reviewPendingPage(f *reviewSignalFixture, fields map[string]any) sessionWire {
	f.t.Helper()
	response, _ := f.owner.request("operator_review", fields)
	return response
}

func reviewPendingSeed(f *reviewSignalFixture) {
	f.t.Helper()
	_, err := f.st.PeerMessages().SetReviewGrant(context.Background(), f.key, store.ReviewGrantChange{Agents: []string{"reviewer-0", "reviewer-1", "reviewer-2"}, Mode: store.ReviewAsk, Now: time.Now()})
	operatorNoError(f.t, err)
	at := time.Now()
	for i := 1; i <= 35; i++ {
		m := reviewSignalAsk(fmt.Sprintf("pending-%02d", i), uint64(i), at)
		m.To = []string{fmt.Sprint("reviewer-", i%3)}
		f.receive(m)
	}
}

func reviewPendingCheckItem(f *reviewSignalFixture, item protocol.ReviewAwaitingItem) {
	f.t.Helper()
	var cursor int64
	operatorNoError(f.t, f.db.QueryRow(`SELECT cursor FROM peer_messages WHERE contact=? AND direction='in' AND message_id=?`, item.Contact, item.MessageID).Scan(&cursor))
	operatorEqual(f.t, "navigation cursor is inbound row", item.Cursor, cursor)
	operatorEqual(f.t, "full canonical identity", item.Contact, f.key)
	if item.Ask == nil {
		f.t.Fatal("pending page lost review pins")
	}
	operatorEqual(f.t, "review head preserved", item.Ask.Head, "head")
}

func reviewPendingEnumerate(f *reviewSignalFixture) int {
	f.t.Helper()
	seen := map[string]bool{}
	before := ""
	for {
		response := reviewPendingPage(f, map[string]any{"review_op": "awaiting", "review_before": before})
		operatorEqual(f.t, "read-only page accepted: "+sessionJSON(response), response.text("type"), "operator_review_result")
		page := sessionDecode[protocol.ReviewAwaitingPage](f.t, response, "review_awaiting")
		operatorEqual(f.t, "total includes unloaded items", page.Total, 35)
		for _, item := range page.Items {
			if seen[item.MessageID] {
				f.t.Fatalf("duplicate awaiting item %q", item.MessageID)
			}
			seen[item.MessageID] = true
			reviewPendingCheckItem(f, item)
		}
		if !page.More {
			operatorEqual(f.t, "last page has no next cursor", page.Next, "")
			break
		}
		operatorEqual(f.t, "bounded page", len(page.Items), 32)
		if page.Next == "" || page.Next == before {
			f.t.Fatal("nonadvancing review cursor")
		}
		before = page.Next
	}
	return len(seen)
}

func TestReviewPendingPagesAcceptanceAwaitingPaginationIsReadOnly(t *testing.T) {
	f := newReviewSignalFixture(t)
	reviewPendingSeed(f)
	token := f.field("awaiting_token")
	operatorEqual(t, "every awaiting item remains reachable", reviewPendingEnumerate(f), 35)
	operatorEqual(t, "viewing does not clear count", f.field("awaiting_you"), "35")
	operatorEqual(t, "viewing does not change set identity", f.field("awaiting_token"), token)
	operatorEqual(t, "viewing never dispatches", f.count("messages"), 0)
	var waiting int
	operatorNoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM remote_imports WHERE state='awaiting'`).Scan(&waiting))
	operatorEqual(t, "viewing never approves", waiting, 35)
}

func TestReviewPendingPagesAcceptanceInvalidFieldsNeverMutate(t *testing.T) {
	for _, op := range []string{"awaiting", "asked"} {
		for _, field := range []map[string]any{{"review_before": "%%%not-a-cursor"}, {"review_id": "not-a-selector"}, {"contact_key": "friendly"}, {"review_reason": "not-a-decision"}} {
			t.Run(op+sessionJSON(field), func(t *testing.T) {
				f := newReviewSignalFixture(t)
				field["review_op"] = op
				response := reviewPendingPage(f, field)
				operatorEqual(t, "invalid read rejected", response.text("code"), "malformed_frame")
				operatorEqual(t, "no review created", f.count("reviews"), 0)
				operatorEqual(t, "no peer message created", f.count("peer_messages"), 0)
			})
		}
	}
}

func TestReviewPendingPagesAcceptanceEmptyPagesHaveExplicitShape(t *testing.T) {
	f := newReviewSignalFixture(t)
	for _, op := range []string{"awaiting", "asked"} {
		response := reviewPendingPage(f, map[string]any{"review_op": op})
		operatorEqual(t, "empty read succeeds", response.text("type"), "operator_review_result")
		page := sessionDecode[map[string]any](t, response, "review_"+op)
		operatorEqual(t, "zero total present", page["total"], any(float64(0)))
		operatorEqual(t, "more false present", page["more"], any(false))
		operatorEqual(t, "next empty present", page["next"], any(""))
		if op == "awaiting" {
			operatorEqual(t, "empty set token remains present", page["token"], any(""))
		}
		items, ok := page["items"].([]any)
		operatorEqual(t, "empty array rather than null", ok, true)
		operatorEqual(t, "zero items", len(items), 0)
	}
}

func TestReviewPendingPagesAcceptanceAgentCannotInspectOperatorQueue(t *testing.T) {
	f := newReviewSignalFixture(t)
	agent, _ := roomPeer(f.operatorFixture, "agent", protocol.Version)
	agent.send(map[string]any{"type": "operator_review", "request_id": "unauthorized", "review_op": "awaiting"})
	operatorEqual(t, "operator queue stays private", agent.frame().text("code"), "forbidden")
}
