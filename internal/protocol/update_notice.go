package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

// The host's update notice: which app release the containers run, which is
// published, and the one manual step the operator could take. The host
// observes and decides; the broker only keeps the last report in memory, so
// a restart (and with it every rollout) clears it, and the chat shows it.
// Every field is an enum, a release tag, a commit or a time: nothing in it
// is text a reader could mistake for an instruction, and nothing runs.
const (
	TypeOperatorUpdateNotice       = "operator_update_notice"        // operator → broker: one-shot, the host's report
	TypeOperatorUpdateNoticeResult = "operator_update_notice_result" // broker → operator: the notice kept

	EventUpdateNoticeChanged = "update_notice_changed" // operator session: read the snapshot again

	// CapabilityUpdateNotice: the broker keeps the host's update notice and
	// puts it in the snapshot.
	CapabilityUpdateNotice = "update_notice"

	UpdateCurrent     = "current"
	UpdateAvailable   = "update_available"
	UpdateMixed       = "mixed"
	UpdateUnknown     = "unknown"
	UpdateCheckFailed = "check_failed"

	UpdateActionNone       = "none"
	UpdateActionUpgradeApp = "upgrade_app"
	UpdateActionUpdateHost = "update_host"

	// How fresh the host's knowledge of the latest release is; empty from a
	// host that predates it.
	AvailableFresh       = "fresh"
	AvailableCached      = "cached"
	AvailableStale       = "stale"
	AvailableCheckFailed = "check_failed"

	// Which deployment the report is about: the default one, or one the
	// host names with flags the notice does not carry. Empty is default.
	ScopeDefault = "default"
	ScopeCustom  = "custom"

	// MaxUpdateNoticeBytes bounds the notice as sent.
	MaxUpdateNoticeBytes = 1024
	// updateNoticeSkew is how far ahead of the broker's clock a host's
	// times may be.
	updateNoticeSkew = 5 * time.Minute
)

var (
	updateStates  = map[string]bool{UpdateCurrent: true, UpdateAvailable: true, UpdateMixed: true, UpdateUnknown: true, UpdateCheckFailed: true}
	updateActions = map[string]bool{UpdateActionNone: true, UpdateActionUpgradeApp: true, UpdateActionUpdateHost: true}
	freshness     = map[string]bool{"": true, AvailableFresh: true, AvailableCached: true, AvailableStale: true, AvailableCheckFailed: true}
	scopes        = map[string]bool{"": true, ScopeDefault: true, ScopeCustom: true}
	releaseTagRE  = regexp.MustCompile(`^workspace-v0\.0\.0-build\.[1-9][0-9]{0,8}\.[1-9][0-9]{0,3}$`)
	commitRE      = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

var errUpdateNotice = errors.New("update_notice: malformed")

// UpdateNotice is the host's report. Empty strings are unknown.
type UpdateNotice struct {
	State            string `json:"state"`
	Action           string `json:"action"`
	AppRelease       string `json:"app_release,omitempty"`
	AppCommit        string `json:"app_commit,omitempty"`
	HostRelease      string `json:"host_release,omitempty"`
	AvailableRelease string `json:"available_release,omitempty"`
	AvailableState   string `json:"available_state,omitempty"`
	Scope            string `json:"scope,omitempty"`
	CheckedAt        string `json:"checked_at,omitempty"`
	ObservedAt       string `json:"observed_at"`
}

// UnmarshalJSON refuses fields the notice does not define, so a command or
// any other free text cannot ride along.
func (n *UpdateNotice) UnmarshalJSON(data []byte) error {
	if len(data) > MaxUpdateNoticeBytes {
		return errUpdateNotice
	}
	type plain UpdateNotice
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var out plain
	if err := decoder.Decode(&out); err != nil {
		return errUpdateNotice
	}
	*n = UpdateNotice(out)
	return nil
}

// Validate reports a notice that is not entirely enums, release tags,
// commits and times no later than now allows.
func (n UpdateNotice) Validate(now time.Time) error {
	for _, valid := range []bool{n.validEnums(), n.validTags(), n.validTimes(now)} {
		if !valid {
			return errUpdateNotice
		}
	}
	return nil
}

func (n UpdateNotice) validEnums() bool {
	return updateStates[n.State] && updateActions[n.Action] && freshness[n.AvailableState] && scopes[n.Scope]
}

func (n UpdateNotice) validTags() bool {
	for _, tag := range []string{n.AppRelease, n.HostRelease, n.AvailableRelease} {
		if tag != "" && !releaseTagRE.MatchString(tag) {
			return false
		}
	}
	return n.AppCommit == "" || commitRE.MatchString(n.AppCommit)
}

func (n UpdateNotice) validTimes(now time.Time) bool {
	return validNoticeTime(n.ObservedAt, now, false) && validNoticeTime(n.CheckedAt, now, true)
}

// ObservedBefore reports n observed strictly before other; Validate has
// checked both times parse.
func (n UpdateNotice) ObservedBefore(other UpdateNotice) bool {
	at, err := time.Parse(time.RFC3339, n.ObservedAt)
	kept, keptErr := time.Parse(time.RFC3339, other.ObservedAt)
	return err == nil && keptErr == nil && at.Before(kept)
}

func validNoticeTime(value string, now time.Time, optional bool) bool {
	if value == "" {
		return optional
	}
	at, err := time.Parse(time.RFC3339, value)
	return err == nil && !at.After(now.Add(updateNoticeSkew))
}
