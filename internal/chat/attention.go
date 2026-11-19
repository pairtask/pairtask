package chat

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Attention (#422): /dnd @AGENT [on|off] sets or shows one local agent's
// do-not-disturb, and /urgent @AGENT TEXT sends that agent one message
// that interrupts it anyway. Both name exactly one agent of this
// workspace, and neither touches the draft in the composer. Urgency
// belongs to that one send: the next message is ordinary again.

const kindAttention = "attention"

var (
	errDNDArg    = errors.New("expects @AGENT, then on or off, or nothing to show the setting: one agent of this workspace")
	errUrgentArg = errors.New("expects @AGENT and the text: one agent of this workspace")
)

// localAgent is the one local agent a tag names: @ID, never @all and
// never an agent of another workspace.
func localAgent(tag string) (string, bool) {
	id, tagged := strings.CutPrefix(tag, "@")
	if !tagged || id == "all" || strings.Contains(id, "@") {
		return "", false
	}
	return id, protocol.ValidateAgentID(id) == nil
}

// dndParts are /dnd's agent and operation; status when none is given.
func dndParts(a argument) (agent, op string, ok bool) {
	fields := strings.Fields(string(a))
	if len(fields) == 0 || len(fields) > 2 {
		return "", "", false
	}
	if agent, ok = localAgent(fields[0]); !ok {
		return "", "", false
	}
	if len(fields) == 1 {
		return agent, protocol.AttentionStatus, true
	}
	op = fields[1]
	return agent, op, op == protocol.AttentionOn || op == protocol.AttentionOff
}

func dndArg(a argument) error {
	if _, _, ok := dndParts(a); !ok {
		return errDNDArg
	}
	return nil
}

// dndCommand asks the broker to set or show the agent's DND.
func (m *Model) dndCommand(a argument) tea.Cmd {
	agent, op, _ := dndParts(a)
	if !m.ready() {
		m.status = "not connected"
		return nil
	}
	return m.request(kindAttention, protocol.Message{Type: protocol.TypeOperatorAttention, AgentID: agent, AttentionOp: op}, agent)
}

// attentionResult shows the setting, or why the broker refused.
func (m *Model) attentionResult(_ pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeOperatorAttentionResult || resp.AttentionState == nil {
		m.status = "do-not-disturb: " + brokerReason(resp)
		return nil
	}
	m.status = dndStatus(*resp.AttentionState)
	return nil
}

// dndStatus is the setting in one line: on or off, whether it is enforced
// now, and what is waiting.
func dndStatus(s protocol.AttentionState) string {
	line := "@" + safe(s.AgentID) + ": do-not-disturb off"
	if s.DND {
		line = "@" + safe(s.AgentID) + ": do-not-disturb on" + dndCaveat(s.Reason)
	}
	return fmt.Sprintf("%s · %d quiet, unacknowledged · inbox %d/%d", line, s.Pending, s.InboxUsed, s.InboxLimit)
}

// dndCaveat says why a setting that is on isn't keeping the agent quiet.
func dndCaveat(reason string) string {
	switch reason {
	case "":
		return ""
	case protocol.AttentionReasonOffline:
		return " (not connected; applies when it connects)"
	case protocol.AttentionReasonOldAdapter:
		return " (NOT enforced: its adapter predates do-not-disturb; restart the agent)"
	}
	return " (not enforced: " + safe(reason) + ")"
}

// urgentParts are /urgent's agent and text.
func urgentParts(a argument) (agent, text string, ok bool) {
	tag, text, _ := strings.Cut(strings.TrimSpace(string(a)), " ")
	text = strings.TrimSpace(text)
	if agent, ok = localAgent(tag); !ok || text == "" {
		return "", "", false
	}
	return agent, text, true
}

func urgentArg(a argument) error {
	if _, _, ok := urgentParts(a); !ok {
		return errUrgentArg
	}
	return nil
}

// urgentCommand sends the agent one urgent message, as its own intent;
// the composer's draft and audience stay as they are.
func (m *Model) urgentCommand(a argument) tea.Cmd {
	agent, text, _ := urgentParts(a)
	if reason := m.urgentBlocked(); reason != "" {
		m.status = reason
		return nil
	}
	payload, err := encodeText(text)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	m.send.intent = &intent{ID: uuid.NewString(), Scope: protocol.Message{To: agent}, Payload: payload, State: IntentSending, Urgent: true}
	m.seal(m.send.intent)
	m.status = ""
	return m.sendIntent()
}

// urgentBlocked is why an urgent message can't go now, "" when it can.
func (m *Model) urgentBlocked() string {
	switch {
	case !m.ready():
		return "not connected"
	case m.send.unresolved():
		return "the previous send is still unresolved"
	}
	return ""
}
