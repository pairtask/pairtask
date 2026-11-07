//go:build linux

package server

import (
	"context"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
	"github.com/pairtask/pairtask/internal/store"
)

func staleAgeSettled(t *testing.T, f *outcomeNoticeServer, id string) {
	t.Helper()
	old := time.Now().Add(-store.AskRetention - time.Hour).UTC().Format(time.RFC3339Nano)
	_, err := f.db.Exec(`UPDATE review_asks_out SET state='declined',settled_at=? WHERE ask_id=?`, old, id)
	operatorNoError(t, err)
}

func staleAskExists(t *testing.T, f *outcomeNoticeServer, id string) bool {
	t.Helper()
	_, found, err := f.st.PeerMessages().AskOutFor(context.Background(), f.key, id)
	operatorNoError(t, err)
	return found
}

func TestStaleAskRetentionAcceptanceOnlyImmediateOpenReplacementKeepsParent(t *testing.T) {
	f := staleFinalAsk(t)
	operatorEqual(t, "first replacement queued", staleRawRequest(t, f.agent, staleReplacement(f, "child", "ask")).Stage, protocol.StageAccepted)
	staleAgeSettled(t, f, "ask")
	operatorNoError(t, f.st.PeerMessages().Collect(context.Background(), time.Now()))
	operatorEqual(t, "open child keeps direct parent", staleAskExists(t, f, "ask"), true)
	staleAgeSettled(t, f, "child")
	operatorEqual(t, "next replacement queued", staleRawRequest(t, f.agent, staleReplacement(f, "grandchild", "child")).Stage, protocol.StageAccepted)
	operatorNoError(t, f.st.PeerMessages().Collect(context.Background(), time.Now()))
	operatorEqual(t, "no retained ancestor chain", staleAskExists(t, f, "ask"), false)
	operatorEqual(t, "immediate parent kept", staleAskExists(t, f, "child"), true)
	details := staleAsked(t, f, "child")
	operatorEqual(t, "historical parent ID retained", details["supersedes"], any("ask"))
	operatorEqual(t, "missing details explicit", details["supersedes_pruned"], any(true))
	staleAgeSettled(t, f, "grandchild")
	operatorNoError(t, f.st.PeerMessages().Collect(context.Background(), time.Now()))
	operatorEqual(t, "settled child releases parent", staleAskExists(t, f, "child"), false)
}

func TestStaleAskReplacementAcceptanceChangedParentCannotReuseID(t *testing.T) {
	f := staleFinalAsk(t)
	request := staleReplacement(f, "replacement", "ask")
	operatorEqual(t, "first accepted", staleRawRequest(t, f.agent, request).Stage, protocol.StageAccepted)
	before := f.snapshot()
	staleReplacementAsk(request)["supersedes"] = "different-parent"
	operatorEqual(t, "different parent conflicts", staleRawRequest(t, f.agent, request).Type, protocol.TypeError)
	outcomeNoticeUnchanged(f, before)
	operatorEqual(t, "original link retained", staleAsked(t, f, "replacement")["supersedes"], any("ask"))
}
