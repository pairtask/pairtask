//go:build linux

package server

import (
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestStaleAskReplacementAcceptanceLinksFinalParentAndRetry(t *testing.T) {
	f := staleFinalAsk(t)
	request := staleReplacement(f, "replacement", "ask")
	operatorEqual(t, "replacement queued", staleRawRequest(t, f.agent, request).Stage, protocol.StageAccepted)
	operatorEqual(t, "parent links new ask", staleAsked(t, f, "ask")["superseded_by"], any("replacement"))
	operatorEqual(t, "new ask links parent", staleAsked(t, f, "replacement")["supersedes"], any("ask"))
	before := f.count("peer_messages")
	operatorEqual(t, "identical retry accepted", staleRawRequest(t, f.agent, request).Stage, protocol.StageAccepted)
	operatorEqual(t, "retry queues once", f.count("peer_messages"), before)
}

func TestStaleAskReplacementAcceptanceOpenParentCannotBeReplaced(t *testing.T) {
	f := newOutcomeNoticeServer(t)
	before := f.snapshot()
	response := staleRawRequest(t, f.agent, staleReplacement(f, "replacement", "ask"))
	operatorEqual(t, "open ask refused", response.Type, protocol.TypeError)
	operatorEqual(t, "specific refusal", response.Code, "not_supersedable")
	outcomeNoticeUnchanged(f, before)
}

func TestStaleAskReplacementAcceptanceOnlyOneDistinctReplacementWins(t *testing.T) {
	f := staleFinalAsk(t)
	operatorEqual(t, "first queued", staleRawRequest(t, f.agent, staleReplacement(f, "first", "ask")).Stage, protocol.StageAccepted)
	before := f.snapshot()
	operatorEqual(t, "second refused", staleRawRequest(t, f.agent, staleReplacement(f, "second", "ask")).Code, "not_supersedable")
	outcomeNoticeUnchanged(f, before)
	operatorEqual(t, "winner unchanged", staleAsked(t, f, "ask")["superseded_by"], any("first"))
}

func TestStaleAskReplacementAcceptanceChangedTargetOrRequesterIsDenied(t *testing.T) {
	for _, change := range []string{"reviewer", "repository", "change", "principal"} {
		t.Run(change, func(t *testing.T) {
			f := staleFinalAsk(t)
			request := staleReplacement(f, "replacement", "ask")
			peer := f.agent
			switch change {
			case "reviewer":
				request["to"] = "other"
			case "repository":
				staleReplacementAsk(request)["repository"] = "pairtask/other"
			case "change":
				staleReplacementAsk(request)["change"] = "260"
			case "principal":
				peer = f.peer("other", 26)
				shared := f.send(protocol.Message{Type: protocol.TypeOperatorContact, ContactOp: protocol.ContactOpShare, ContactKey: f.key, ContactAgents: []string{"sender", "other"}, MessageID: "share-other"})
				operatorEqual(t, "intruder otherwise authorized to send", shared.Type, protocol.TypeOperatorContactResult)
			}
			before := f.snapshot()
			operatorEqual(t, "authority or target mismatch refused", staleRawRequest(t, peer, request).Type, protocol.TypeError)
			outcomeNoticeUnchanged(f, before)
		})
	}
}

func TestStaleAskReplacementAcceptanceQueueFailureLeavesParentUntouched(t *testing.T) {
	f := staleFinalAsk(t)
	_, err := f.db.Exec(`CREATE TRIGGER stale_queue_failure BEFORE INSERT ON peer_messages BEGIN SELECT RAISE(ABORT,'injected stale replacement failure'); END`)
	operatorNoError(t, err)
	before := f.snapshot()
	request := staleReplacement(f, "replacement", "ask")
	operatorEqual(t, "queue failure reported", staleRawRequest(t, f.agent, request).Type, protocol.TypeError)
	outcomeNoticeUnchanged(f, before)
	operatorEqual(t, "no orphan parent link", staleAsked(t, f, "ask")["superseded_by"], any(nil))
	_, err = f.db.Exec(`DROP TRIGGER stale_queue_failure`)
	operatorNoError(t, err)
	operatorEqual(t, "retry can succeed", staleRawRequest(t, f.agent, request).Stage, protocol.StageAccepted)
	operatorEqual(t, "committed link", staleAsked(t, f, "ask")["superseded_by"], any("replacement"))
}
