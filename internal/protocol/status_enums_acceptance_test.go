package protocol_test

import (
	"strings"
	"testing"

	"github.com/pairtask/pairtask/internal/protocol"
)

func TestStatusEnumsAcceptanceInvalidValuesListAllowedChoices(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status protocol.AgentStatus
		want   string
	}{
		{"state", protocol.AgentStatus{State: "approved"}, "state must be one of: active, waiting, blocked, idle, done"},
		{"empty state", protocol.AgentStatus{}, "state must be one of: active, waiting, blocked, idle, done"},
		{"waiting_on", protocol.AgentStatus{State: "waiting", WaitingOn: "reviewer"}, "waiting_on must be empty or one of: operator, peer, ci, tools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := protocol.ValidateStatus(tc.status)
			if err == nil {
				t.Fatal("invalid status accepted")
			}
			if err.Error() != tc.want {
				t.Fatalf("error = %q, want %q", err, tc.want)
			}
		})
	}
}

func TestStatusEnumsAcceptanceEveryPublishedChoiceStillValidates(t *testing.T) {
	for _, state := range protocol.StatusStates {
		for _, waiting := range protocol.StatusWaiting {
			if err := protocol.ValidateStatus(protocol.AgentStatus{State: state, WaitingOn: waiting}); err != nil {
				t.Errorf("advertised state=%q waiting_on=%q rejected: %v", state, waiting, err)
			}
		}
	}
}

func TestStatusEnumsAcceptanceTextLimitRemainsBytesNotCharacters(t *testing.T) {
	for _, field := range []string{"task", "step", "ref"} {
		t.Run(field, func(t *testing.T) {
			status := protocol.AgentStatus{State: "active"}
			text := strings.Repeat("é", protocol.StatusFieldBytes/2)
			setStatusEnumText(&status, field, text)
			if err := protocol.ValidateStatus(status); err != nil {
				t.Fatalf("120-byte boundary rejected: %v", err)
			}
			setStatusEnumText(&status, field, text+"é")
			if err := protocol.ValidateStatus(status); err == nil {
				t.Fatal("oversized multibyte text accepted after schema change")
			}
		})
	}
}

func setStatusEnumText(status *protocol.AgentStatus, field, text string) {
	switch field {
	case "task":
		status.Task = text
	case "step":
		status.Step = text
	case "ref":
		status.Ref = text
	}
}
