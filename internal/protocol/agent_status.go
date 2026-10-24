package protocol

import (
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"
)

// Agent status frames: an agent declares what it is doing, at each change,
// never as a heartbeat. Status is informational only: no sequence number, no
// inbox entry, no acknowledgment, and never an authorization.
const (
	TypeAgentStatusSet    = "agent_status_set"    // agent → broker: declare; answered by agent_status_result or error
	TypeAgentStatusGet    = "agent_status_get"    // agent → broker: read one agent's latest record (default: the caller)
	TypeAgentStatusResult = "agent_status_result" // broker → agent: the record, or null when none was declared
	TypeAgentActivity     = "agent_activity"      // launcher/adapter → broker: observed activity, no response

	EventStatusChanged = "status_changed" // operator session: an agent's status or activity changed

	ErrInvalidStatus     = "invalid_status"
	ErrStatusRateLimited = "status_rate_limited" // retryable
)

// Status states and what an agent may be waiting on.
var (
	StatusStates  = []string{"active", "waiting", "blocked", "idle", "done"}
	StatusWaiting = []string{"", "operator", "peer", "ci", "tools"}
	// ActivitySources observe the agent's own session; any other is refused.
	ActivitySources = []string{"codex-launcher", "claude-adapter"}
)

// StatusFieldBytes bounds each free-text status field.
const StatusFieldBytes = 120

// SnapshotStatuses bounds the statuses an operator snapshot carries.
const SnapshotStatuses = 64

// AgentStatus is an agent's latest declaration. Identity, session and times
// are set by the broker; a caller's values for them are ignored.
type AgentStatus struct {
	AgentID    string    `json:"agent_id"`
	SessionID  string    `json:"session_id"`
	State      string    `json:"state"`
	Task       string    `json:"task"`
	Step       string    `json:"step"`
	WaitingOn  string    `json:"waiting_on"`
	Ref        string    `json:"ref"`
	DeclaredAt time.Time `json:"declared_at"`
	Activity   *Activity `json:"activity"`  // observed for the declaring session; null when unavailable
	Connected  bool      `json:"connected"` // the declaring session is the agent's current one
}

// Activity is when the broker last saw activity reported for a session, and
// from where.
type Activity struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"`
}

// ValidateStatus checks a declaration's state, wait reason and text fields.
func ValidateStatus(s AgentStatus) error {
	if !oneOf(s.State, StatusStates) {
		return fmt.Errorf("state must be one of %v", StatusStates)
	}
	if !oneOf(s.WaitingOn, StatusWaiting) {
		return fmt.Errorf("waiting_on must be empty or one of %v", StatusWaiting[1:])
	}
	for name, text := range map[string]string{"task": s.Task, "step": s.Step, "ref": s.Ref} {
		if err := statusText(text); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func statusText(text string) error {
	if len(text) > StatusFieldBytes {
		return fmt.Errorf("at most %d bytes", StatusFieldBytes)
	}
	if !utf8.ValidString(text) {
		return errors.New("not valid UTF-8")
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return errors.New("control characters are not allowed")
		}
	}
	return nil
}

// ValidActivitySource reports whether source is an accepted observer.
func ValidActivitySource(source string) bool { return oneOf(source, ActivitySources) }

func oneOf(value string, allowed []string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}
