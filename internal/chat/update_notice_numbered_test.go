package chat

import (
	"testing"
	"time"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestReleaseNamesReadBothTagForms(t *testing.T) {
	for tag, want := range map[string]string{
		"workspace-v0.0.0-build.1160.1": "build 1160.1",
		"v1165":                         "Pairtask 1165",
		"v1165.1":                       "Pairtask 1165.1",
		"v0":                            "",
		"v1165.0":                       "",
		"1165":                          "",
		"vabc":                          "",
	} {
		if got := buildName(tag); got != want {
			t.Errorf("buildName(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestAnUpdateNoticeWithNewTagsIsShown(t *testing.T) {
	n := protocol.UpdateNotice{State: protocol.UpdateAvailable, Action: protocol.UpdateActionUpgradeApp,
		AppRelease: "workspace-v0.0.0-build.1160.1", HostRelease: "v1165", AvailableRelease: "v1165",
		ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := n.Validate(time.Now()); err != nil {
		t.Fatalf("a notice naming vN releases was refused: %v", err)
	}
	n.AvailableRelease = "v1165.1"
	if err := n.Validate(time.Now()); err != nil {
		t.Fatalf("a hotfix tag was refused: %v", err)
	}
	n.AvailableRelease = "v1165-beta"
	if err := n.Validate(time.Now()); err == nil {
		t.Fatal("a malformed tag was accepted")
	}
}
