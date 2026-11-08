package chat

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/pairtask/pairtask/internal/protocol"
)

// The host's update notice (#78): which app release the containers run,
// which is published, and the manual step the host suggests. The chat only
// shows it. Every word comes from a validated enum or tag and fixed text
// here; nothing in the notice runs, and the update itself happens on the
// host. A custom deployment's flags never reach the broker, so its step is
// read on the host with `pk version`.

const buildTagPrefix = "workspace-v0.0.0-build."

// setUpdateNotice installs a snapshot's notice: none from an older broker,
// or before the host reported one.
func (m *Model) setUpdateNotice(snap *protocol.OperatorSnapshot) {
	m.update = nil
	if n := snap.UpdateNotice; n != nil && n.Validate(time.Now()) == nil {
		notice := *n
		m.update = &notice
	}
}

// updateNoticeChanged reads the snapshot again for the new notice.
func (m *Model) updateNoticeChanged() tea.Cmd { return m.refreshStatuses() }

// updateReminder is the footer's lowest-priority reminder: shown only when
// the host found a step to take.
func (m *Model) updateReminder() string {
	n := m.update
	if n == nil || n.Action == protocol.UpdateActionNone {
		return ""
	}
	head := "update: " + buildName(n.AvailableRelease) + " available"
	switch {
	case n.State == protocol.UpdateMixed:
		head += " (containers differ)"
	case n.AppRelease != "":
		head += " (running " + strings.TrimPrefix(buildName(n.AppRelease), "build ") + ")"
	}
	return bandWarn.Render(head) + bandStyle.Render(" · "+hostStep(n))
}

// hostStep is the fixed words for the notice's action.
func hostStep(n *protocol.UpdateNotice) string {
	switch {
	case n.Scope == protocol.ScopeCustom:
		return "on the host: pk version with the same --project/--remote/--repo shows the command"
	case n.Action == protocol.UpdateActionUpgradeApp:
		return "host: pk upgrade"
	case n.Action == protocol.UpdateActionUpdateHost:
		return "host: rerun the installer (pk version shows it)"
	}
	return ""
}

func (m *Model) versionCommand(argument) tea.Cmd {
	m.status = m.versionLine(time.Now())
	return nil
}

// versionLine is /version: what the host last reported, how old the check
// of the latest release is, and the step, all from the notice.
func (m *Model) versionLine(now time.Time) string {
	if !m.session.HasCapability(protocol.CapabilityUpdateNotice) {
		return "version: this broker does not report versions"
	}
	n := m.update
	if n == nil {
		return "version: not reported yet (run pk version on the host)"
	}
	parts := []string{"version: app " + appVersion(n), "host " + orUnknown(buildName(n.HostRelease)),
		"latest " + orUnknown(buildName(n.AvailableRelease)) + " (" + checkedText(n, now) + ")"}
	switch {
	case n.Action != protocol.UpdateActionNone:
		parts = append(parts, hostStep(n))
	case n.State == protocol.UpdateCurrent:
		parts = append(parts, "up to date")
	default:
		parts = append(parts, "no step suggested; pk version on the host has details")
	}
	return strings.Join(parts, " · ")
}

func appVersion(n *protocol.UpdateNotice) string {
	switch {
	case n.State == protocol.UpdateMixed:
		return "mixed (containers differ)"
	case n.AppRelease != "":
		return buildName(n.AppRelease)
	case n.AppCommit != "":
		return "unknown (commit " + n.AppCommit[:12] + ")"
	}
	return "unknown"
}

// checkedText is the age of the latest-release check, and whether the host
// called its knowledge stale or failed.
func checkedText(n *protocol.UpdateNotice, now time.Time) string {
	text := "never checked"
	if at, err := time.Parse(time.RFC3339, n.CheckedAt); err == nil {
		text = "checked " + ago(now.Sub(at)) + " ago"
	}
	switch n.AvailableState {
	case protocol.AvailableStale:
		text += ", stale"
	case protocol.AvailableCheckFailed:
		text += ", check failed"
	}
	return text
}

func ago(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return "<1m"
}

// buildName shortens a release tag to "build N.M"; other text is never shown.
func buildName(tag string) string {
	if !strings.HasPrefix(tag, buildTagPrefix) {
		return ""
	}
	return "build " + strings.TrimPrefix(tag, buildTagPrefix)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
