package chat

import (
	"encoding/json"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Send intent states. Nothing is shown as accepted until the broker says so.
const (
	IntentSending  = "sending"
	IntentUnknown  = "unknown"
	IntentAccepted = "accepted"
	IntentRejected = "rejected"
)

// intent is a frozen send: retries reuse exactly this ID, scope, payload and
// correlation. Editing the draft never changes it.
type intent struct {
	ID         string
	Scope      protocol.Message
	Payload    json.RawMessage
	InReplyTo  string
	State      string
	Recipients []protocol.Recipient
	Excluded   []string
	Detail     string
	Ack        *ackBinding  // the reply's automatic acknowledgment, consumed once
	AckBatch   []ackBinding // a direct or group send's audience acknowledgments, consumed once
}

// ackBinding ties an automatic acknowledgment to a message as it was when
// the send was dispatched: the same ID, content and epoch, or no
// acknowledgment. sender is the agent whose acceptance as a recipient makes
// an audience binding count.
type ackBinding struct {
	id          string
	seq         uint64
	hash, epoch string
	sender      string
}

type sendState struct {
	intent *intent
}

func (s *sendState) unresolved() bool {
	return s.intent != nil && (s.intent.State == IntentSending || s.intent.State == IntentUnknown)
}

// disconnected leaves an in-flight send with an unknown outcome.
func (s *sendState) disconnected() {
	if s.intent != nil && s.intent.State == IntentSending {
		s.intent.State = IntentUnknown
	}
}

// submit freezes the draft, its audience and reply into a send intent. The
// hint line shows the audience beforehand, so there is no confirmation.
func (m *Model) submit() tea.Cmd {
	scope, reason, ok := m.sendable()
	if !ok {
		if reason != "" {
			m.status = reason
		}
		return nil
	}
	if !m.ready() {
		m.status = "not connected; the draft is kept"
		return nil
	}
	if m.send.unresolved() {
		m.status = "the previous send is still unresolved"
		return nil
	}
	payload, err := encodeText(m.outgoingText())
	if err != nil {
		m.status = err.Error()
		return nil
	}
	inReplyTo := ""
	if r := m.compose.reply; r != nil {
		inReplyTo = r.messageID
	}
	return m.dispatch(scope, payload, inReplyTo)
}

// sendable is the draft's scope, or why it can't be sent now. An empty draft
// is not sendable and needs no reason.
func (m *Model) sendable() (protocol.Message, string, bool) {
	switch {
	case m.compose.draftEmpty():
		return protocol.Message{}, "", false
	case !m.draftBody():
		return protocol.Message{}, "message is empty", false
	case m.tooSmall():
		return protocol.Message{}, "terminal too small to send", false
	}
	aud := m.resolveAudience()
	if aud.err != "" {
		return protocol.Message{}, aud.err, false
	}
	scope, err := aud.scope()
	if err != nil {
		return protocol.Message{}, err.Error(), false
	}
	return scope, "", true
}

// encodeText builds the payload and enforces the operator payload cap on its
// encoded form, before anything is sent.
func encodeText(text string) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(json.RawMessage(payload))
	if err != nil {
		return nil, err
	}
	if len(encoded) > protocol.OperatorPayloadMax {
		return nil, errTooLarge
	}
	return payload, nil
}

// dispatch queues the frozen intent; only once it is queued does the composer
// let go of the draft and its audience.
func (m *Model) dispatch(scope protocol.Message, payload json.RawMessage, inReplyTo string) tea.Cmd {
	m.send.intent = &intent{ID: uuid.NewString(), Scope: scope, Payload: payload, InReplyTo: inReplyTo, State: IntentSending, Ack: m.bindAck(), AckBatch: m.bindAudience(scope, inReplyTo)}
	m.reopenScope(scope) // writing to the same recipients shows their closed conversation
	cmd := m.sendIntent()
	m.compose.resetDraft()
	m.endRecall()
	m.status = ""
	m.follow()
	return cmd
}

func (m *Model) sendIntent() tea.Cmd {
	in := m.send.intent
	in.State = IntentSending
	msg := protocol.Message{Type: protocol.TypeOperatorSend, MessageID: in.ID, To: in.Scope.To, Group: in.Scope.Group, Payload: in.Payload, InReplyTo: in.InReplyTo}
	return m.request(kindSend, msg, in.ID)
}

// resendUnknown retries an unknown send with exactly the same intent.
func (m *Model) resendUnknown() tea.Cmd {
	if m.send.intent == nil || m.send.intent.State != IntentUnknown {
		return nil
	}
	return m.sendIntent()
}

func (m *Model) sendResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	in := m.send.intent
	if in == nil || in.ID != req.ids[0] {
		return nil
	}
	m.quitArmed = false
	if sendAccepted(resp, in.ID) {
		in.State, in.Recipients, in.Excluded = IntentAccepted, resp.Recipients, resp.Excluded
		return tea.Batch(m.startCatchup(), m.autoAck(in), m.autoAckBatch(in))
	}
	// Only an explicit broker error rejects; anything else leaves the outcome unknown.
	if resp.Type != protocol.TypeError {
		in.State = IntentUnknown
		return m.malformed("send response: " + brokerReason(resp))
	}
	// The outcome updates the intent only; the composer may hold a newer draft.
	in.State, in.Detail = IntentRejected, brokerReason(resp)
	return nil
}

func sendAccepted(resp protocol.Message, id string) bool {
	if resp.Type != protocol.TypeAck {
		return false
	}
	return resp.Stage == protocol.StageAccepted && resp.MessageID == id
}

func payloadText(payload json.RawMessage) string {
	var body struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(payload, &body) == nil && body.Text != nil {
		return *body.Text
	}
	return string(payload)
}

// agentView is the current presence baseline; omitted agents are unknown.
type agentView struct {
	list    []protocol.AgentPresence
	total   int
	partial bool
}

func (a *agentView) apply(ev *protocol.SessionEvent) {
	for i, p := range a.list {
		if p.AgentID != ev.AgentID {
			continue
		}
		if ev.State == "disconnected" {
			a.list = append(a.list[:i], a.list[i+1:]...)
			a.total--
			return
		}
		a.list[i] = protocol.AgentPresence{AgentID: ev.AgentID, SessionID: ev.SessionID, ProtocolVersion: ev.ProtocolVersion}
		return
	}
	if ev.State == "connected" {
		a.list = append(a.list, protocol.AgentPresence{AgentID: ev.AgentID, SessionID: ev.SessionID, ProtocolVersion: ev.ProtocolVersion})
		a.total++
	}
}
