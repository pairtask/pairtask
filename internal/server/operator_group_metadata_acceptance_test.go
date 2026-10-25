//go:build linux

package server

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func groupMetadataMatches(t *testing.T, where string, msg protocol.Message, members []string) {
	t.Helper()
	if !reflect.DeepEqual(msg.Group, members) {
		t.Errorf("%s: group members = %v, want %v", where, msg.Group, members)
	}
}

func TestOperatorGroupMetadataCanonicalAcrossDeliveryAndHistory(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a, b := f.peer("alpha", protocol.Version), f.peer("beta", protocol.Version)
	for _, id := range []string{"first-group", "same-people-again"} {
		req := operatorRequest(id, "")
		req.Group = []string{"beta", "alpha", "alpha"}
		if id == "same-people-again" {
			req.Group = []string{"alpha", "beta"}
		}
		requireOperatorAccepted(t, f.send(req), "alpha", "beta")
		for _, peer := range []*operatorPeer{a, b} {
			msg := peer.read()
			operatorEqual(t, "original message ID", msg.MessageID, id)
			operatorEqual(t, "operator provenance", msg.Origin, protocol.OriginOperator)
			groupMetadataMatches(t, "live delivery", msg, []string{"alpha", "beta"})
		}
	}
	page := historyRead(f, 0, 0, 32)
	operatorEqual(t, "both distinct messages retained", len(page.Entries), 2)
	for _, entry := range page.Entries {
		groupMetadataMatches(t, "history", entry.Message, []string{"alpha", "beta"})
	}
}

func TestOperatorGroupMetadataDoesNotInferGroupFromAgentName(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	a := f.peer("group:foo", protocol.Version)
	requireOperatorAccepted(t, f.send(operatorRequest("literal-group-agent", "group:foo")), "group:foo")
	live := a.read()
	history := historyRead(f, 0, 0, 32).Entries[0].Message
	for _, msg := range []protocol.Message{live, history} {
		operatorEqual(t, "literal recipient unchanged", msg.To, "group:foo")
		operatorEqual(t, "direct send has no group metadata", len(msg.Group), 0)
	}
}

func TestOperatorGroupMetadataSurvivesRestartAndReplay(t *testing.T) {
	db := filepath.Join(t.TempDir(), "groups.db")
	t.Run("before", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), db)
		a, b := f.peer("alpha", protocol.Version), f.peer("beta", protocol.Version)
		req := operatorRequest("persistent-group", "")
		req.Group = []string{"beta", "alpha"}
		requireOperatorAccepted(t, f.send(req), "alpha", "beta")
		a.read()
		b.read()
	})
	t.Run("after", func(t *testing.T) {
		f := newOperatorFixture(t, os.Getuid(), db)
		a := f.peer("alpha", protocol.Version)
		replayed := a.read()
		operatorEqual(t, "original message replayed", replayed.MessageID, "persistent-group")
		groupMetadataMatches(t, "replay", replayed, []string{"alpha", "beta"})
		page := historyRead(f, 0, 0, 32)
		operatorEqual(t, "one persisted group message", len(page.Entries), 1)
		groupMetadataMatches(t, "history after restart", page.Entries[0].Message, []string{"alpha", "beta"})
	})
}
