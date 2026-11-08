//go:build linux

package server

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// A refresh that finishes after a newer report never puts the older state
// back; a report observed at the same moment, or later, replaces it.
func TestUpdateNoticeOlderObservationIsRefused(t *testing.T) {
	f := newOperatorFixture(t, os.Getuid(), "")
	newer := manualOperatorNotice()
	newer["observed_at"] = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	operatorEqual(t, "newer kept", manualOperatorSet(t, f, newer).text("type"), "operator_update_notice_result")

	older := manualOperatorNotice()
	older["observed_at"] = time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano)
	older["available_release"] = "workspace-v0.0.0-build.98.1"
	operatorEqual(t, "older refused", manualOperatorSet(t, f, older).text("type"), "error")

	same := manualOperatorNotice()
	same["observed_at"] = newer["observed_at"]
	same["state"], same["action"] = "current", "none"
	operatorEqual(t, "same moment replaces", manualOperatorSet(t, f, same).text("type"), "operator_update_notice_result")

	kept := manualOperatorSnapshot(t, sessionOpen(f))
	var notice sessionWire
	operatorNoError(t, json.Unmarshal(kept["update_notice"], &notice))
	operatorEqual(t, "latest report kept", notice.text("state"), "current")
	operatorEqual(t, "older release never shown", notice.text("available_release"), "workspace-v0.0.0-build.100.1")
}
