package chat

import (
	"sort"

	"github.com/pairtask/pairtask/internal/protocol"
)

// StateView is a read-only snapshot of the chat for tests and the CLI.
type StateView struct {
	Connection        string
	Session           protocol.SessionInfo
	Epoch             string
	Revision          uint64
	Baseline          bool
	Entries           []EntryView
	Agents            []protocol.AgentPresence
	AgentsTotal       int
	AgentsPartial     bool
	InboxPending      int
	InboxHeadSeq      uint64
	Unread            int
	Paused            bool
	Intent            *IntentView
	QueuedAcks        []QueuedAckView
	InFlight          int
	Queued            int
	InboxUsage        *protocol.InboxStatus
	BulkAck           *BulkAckView
	Conversations     []ConversationView
	Recall            *RecallView
	Selected          string // the selected conversation's agent ID; empty for the common chat
	SelectedAll       bool
	HiddenViews       int // conversations closed with /close
	HiddenUnread      int // unread messages in hidden conversations
	Draft             string
	Pasted            bool
	SendGuard         string // the draft text held as command-like; Enter again sends it (#208)
	Audience          AudienceView
	InReplyTo         string
	ReplySender       string
	CommandLine       *CommandLineView
	CommandTrusted    bool // the open command line's name was typed, so Enter may run it (#208)
	Picker            *PickerView
	Hint              string
	ScrollOffset      int
	Cutoff            uint64
	Status            string
	Help              bool
	Panel             string                // the open panel\'s kind, "" for none
	Reviews           []protocol.ReviewView // the last reviews listed or requested (#201)
	ReviewSelected    string                // the review shown in detail
	TooSmall          bool
	KeyDisambiguation bool
	ExitCode          int
	Fatal             string
}

// AudienceView is who the draft would go to, or why it can't be sent.
type AudienceView struct {
	Everyone bool
	IDs      []string
	NotSeen  []string
	Source   string
	Error    string
}

// CommandLineView is the open command line.
type CommandLineView struct {
	Text       string
	Candidates []string
	Error      string
}

// PickerView is the open @ or / picker; Items are raw IDs or command names.
type PickerView struct {
	Kind     string
	Items    []string
	Selected int
}

// EntryView is one timeline message.
type EntryView struct {
	MessageID       string
	Seq             uint64
	Direction       string
	From            string
	To              string
	Origin          string
	InReplyTo       string
	Body            string
	BodyOmitted     bool
	Receipt         string
	OperatorStage   string
	Ack             string // received: AckNeeded, AckQueued or AckDone
	Stages          []protocol.RecipientStage
	StagesTruncated bool
	Conflict        bool
	GapBelow        bool
	Claims          []protocol.Claim // an operator-sent message's task claims (#141)
}

type IntentView struct {
	MessageID  string
	Scope      protocol.Message
	InReplyTo  string
	State      string
	Recipients []protocol.Recipient
	Excluded   []string
	Detail     string
}

type QueuedAckView struct {
	MessageID string
	Suspended string
}

// State returns a copy; changing it doesn't affect the model.
func (m *Model) State() StateView {
	c := &m.compose
	v := StateView{
		Connection: m.conn, Session: m.session, Epoch: m.epoch, Revision: m.revision, Baseline: m.baseline,
		Agents:      append([]protocol.AgentPresence(nil), m.agents.list...),
		AgentsTotal: m.agents.total, AgentsPartial: m.agents.partial, InboxPending: m.inbox.pending,
		InboxHeadSeq: m.inbox.headSeq, Unread: m.unreadIn(allKey), Paused: m.timeline.paused,
		Conversations: m.conversationViews(), Selected: m.selectedID(), SelectedAll: m.conv.selected.common(), Recall: m.recallView(),
		InFlight: m.inFlight(), Queued: len(m.outbox), InboxUsage: m.usage, BulkAck: m.bulkView(), Draft: c.editor.Value(), Pasted: c.pasted,
		Audience: m.audienceView(), Hint: m.hintText(), ScrollOffset: m.scrollOffset(), Cutoff: m.cutoff,
		Status: m.status, Help: m.panel != nil && m.panel.kind == panelHelp, TooSmall: m.tooSmall(),
		Reviews: append([]protocol.ReviewView(nil), m.reviews.list...), ReviewSelected: m.reviews.selected,
		KeyDisambiguation: m.keyboard, ExitCode: m.exitCode, Fatal: m.fatal,
	}
	if m.panel != nil {
		v.Panel = m.panel.kind
	}
	m.composeState(&v)
	v.HiddenViews, v.HiddenUnread = m.hiddenCounts()
	for _, e := range m.timeline.order {
		ev := e.view()
		ev.Claims = append([]protocol.Claim(nil), m.claimsOf(e)...)
		if e.direction == "received" {
			ev.Ack = m.ackState(e)
		}
		v.Entries = append(v.Entries, ev)
	}
	if in := m.send.intent; in != nil {
		v.Intent = &IntentView{MessageID: in.ID, Scope: in.Scope, InReplyTo: in.InReplyTo, State: in.State, Recipients: in.Recipients, Excluded: in.Excluded, Detail: in.Detail}
	}
	for id, q := range m.inbox.queuedAcks {
		v.QueuedAcks = append(v.QueuedAcks, QueuedAckView{MessageID: id, Suspended: q.suspended})
	}
	sort.Slice(v.QueuedAcks, func(i, j int) bool { return v.QueuedAcks[i].MessageID < v.QueuedAcks[j].MessageID })
	return v
}

// composeState fills the composer's reply, command line and picker.
func (m *Model) composeState(v *StateView) {
	c := &m.compose
	if r := c.reply; r != nil {
		v.InReplyTo, v.ReplySender = r.messageID, r.sender
	}
	v.SendGuard = c.guard
	if c.cmd != nil {
		v.CommandTrusted = c.cmd.trusted()
		v.CommandLine = &CommandLineView{Text: c.cmd.input.Value(), Error: m.status}
		if p := c.openPicker(PickerCommand); p != nil {
			v.CommandLine.Candidates = append([]string(nil), p.items...)
		}
	}
	if p := c.picker; p != nil {
		v.Picker = &PickerView{Kind: p.kind, Items: append([]string(nil), p.items...), Selected: p.selected}
	}
}

func (m *Model) audienceView() AudienceView {
	a := m.resolveAudience()
	return AudienceView{Everyone: a.everyone, IDs: append([]string(nil), a.ids...), NotSeen: append([]string(nil), a.notSeen...), Source: a.source, Error: a.err}
}

func (e *entry) view() EntryView {
	v := EntryView{MessageID: e.msg.MessageID, Seq: e.msg.Seq, Direction: e.direction, From: e.msg.From, To: e.msg.To,
		Origin: protocol.DisplayOrigin(e.msg.Origin), InReplyTo: e.msg.InReplyTo, BodyOmitted: e.omitted,
		Receipt: e.receipt, OperatorStage: e.operatorStage, StagesTruncated: e.stagesTruncated, Conflict: e.conflict, GapBelow: e.gapBelow}
	if !e.omitted {
		v.Body = payloadText(e.msg.Payload)
	}
	for _, st := range e.stages {
		v.Stages = append(v.Stages, st.RecipientStage)
	}
	sort.Slice(v.Stages, func(i, j int) bool { return v.Stages[i].AgentID < v.Stages[j].AgentID })
	return v
}
