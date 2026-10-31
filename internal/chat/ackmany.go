package chat

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Request kinds for inbox management.
const (
	kindInboxStatus = "inbox_status"
	kindAckMany     = "ack_many"
)

// bulkAck is one /ack all or /ack from run: the inbox status read first, then
// stable pages under a frozen upper bound.
type bulkAck struct {
	token   string // ties the status read and the pages to this run
	from    string
	sel     protocol.AckSelection
	running bool
	result  protocol.AckManyResult
}

// Request tokens: a bulk run's status read is its own, never one triggered
// by a refusal notice.
const statusForHeader = "header"

// BulkAckView is the state of a bulk acknowledgment, for tests and the CLI.
type BulkAckView struct {
	Running                                    bool
	Acknowledged, AlreadyAcknowledged, Skipped int
}

// ackCommand: /ack all and /ack from <id> acknowledge in bulk, /ack #n one
// message. Bare /ack offers these in the picker before it runs (offersWord).
func (m *Model) ackCommand(arg argument) tea.Cmd {
	switch {
	case arg.empty():
		m.status = "pick what to acknowledge: /ack all, /ack #n or /ack from AGENT"
		return nil
	case arg == "all":
		return m.startBulkAck("")
	case strings.HasPrefix(string(arg), "from "):
		return m.startBulkAck(strings.TrimSpace(strings.TrimPrefix(string(arg), "from ")))
	}
	e, reason := m.target(arg, noEntry)
	if e == nil {
		m.status = reason
		return nil
	}
	return m.acknowledge(e)
}

// ackArg accepts nothing, "all", "from <id>" or "#n".
func ackArg(a argument) error {
	s := string(a)
	switch {
	case a.empty(), s == "all":
		return nil
	case strings.HasPrefix(s, "from "):
		if err := protocol.ValidateAgentID(strings.TrimSpace(strings.TrimPrefix(s, "from "))); err != nil {
			return err
		}
		return nil
	}
	return targetArg(a)
}

// ackLabel renders one picker row: "#seq sender · first line".
func (m *Model) ackLabel(id string) string {
	e := m.timeline.entries[id]
	if e == nil {
		return "  " + safe(id)
	}
	line, _, _ := strings.Cut(payloadText(e.msg.Payload), "\n")
	if e.omitted {
		line = "[text not available]"
	}
	return fmt.Sprintf("  #%d %s · %s", e.msg.Seq, agentStyle(e.msg.From).Render(safe(e.msg.From)), safeText(line))
}

// startBulkAck reads the inbox status first, so the pages run under a frozen
// upper bound and start just before the oldest unacknowledged row.
func (m *Model) startBulkAck(from string) tea.Cmd {
	if !m.ready() {
		m.status = "not connected"
		return nil
	}
	if m.bulk != nil && m.bulk.running {
		m.status = "a bulk acknowledgment is already running"
		return nil
	}
	m.bulkSeq++
	m.bulk = &bulkAck{token: fmt.Sprintf("bulk-%d", m.bulkSeq), from: from, running: true}
	return m.request(kindInboxStatus, protocol.Message{Type: protocol.TypeOperatorInboxStatus}, m.bulk.token)
}

// inboxStatusResponse keeps the usage for the header and, when a bulk run
// is waiting for it, starts the first page.
func (m *Model) inboxStatusResponse(req pendingRequest, resp protocol.Message) tea.Cmd {
	b := m.bulk
	forBulk := b != nil && b.running && req.is(kindInboxStatus, b.token)
	if resp.Type != protocol.TypeOperatorInboxStatus || resp.Inbox == nil {
		if forBulk {
			return m.bulkFailed("inbox status not read: " + brokerReason(resp))
		}
		m.status = "inbox status not read: " + brokerReason(resp)
		return nil
	}
	m.usage = resp.Inbox
	if !forBulk {
		return m.refreshUsage(false) // a change that landed while this read was in flight
	}
	if hint := m.fromHint(b.from, *resp.Inbox); hint != "" {
		return m.bulkFailed(hint)
	}
	return m.startBulkPages(*resp.Inbox)
}

// startBulkPages freezes the run's bounds from its own status read and
// requests the first page, or ends a run with nothing to do.
func (m *Model) startBulkPages(status protocol.InboxStatus) tea.Cmd {
	b := m.bulk
	if b.sel.UpperSeq != 0 {
		return nil // already started
	}
	if status.Unacknowledged == 0 || status.HeadSeq == 0 {
		m.bulk = nil
		m.status = "nothing to acknowledge"
		return nil
	}
	b.sel = protocol.AckSelection{From: b.from, UpperSeq: status.HeadSeq, Limit: protocol.AckManyDefault}
	if status.OldestUnacknowledgedSeq > 0 {
		b.sel.AfterSeq = status.OldestUnacknowledgedSeq - 1
	}
	return m.ackManyPage()
}

func (m *Model) ackManyPage() tea.Cmd {
	sel := m.bulk.sel
	return m.request(kindAckMany, protocol.Message{Type: protocol.TypeOperatorAckMany, AckSelection: &sel}, m.bulk.token)
}

// ackManyResponse applies one page: receipts mark rows acknowledged, then the
// next page continues under the same upper bound, or the run ends.
func (m *Model) ackManyResponse(req pendingRequest, resp protocol.Message) tea.Cmd {
	b := m.bulk
	if b == nil || !req.is(kindAckMany, b.token) {
		return nil // a page from an earlier run: nothing is applied from it
	}
	if resp.Type == protocol.TypeError {
		return m.bulkFailed("bulk acknowledgment failed: " + brokerReason(resp))
	}
	if err := protocol.ValidateAckMany(b.sel, resp); err != nil {
		return m.bulkFailed("bulk acknowledgment returned an invalid result; nothing applied: " + err.Error())
	}
	for _, r := range resp.Receipts {
		if e := m.timeline.entries[r.MessageID]; e != nil {
			e.operatorStage = protocol.StageAgentAcknowledged
		}
	}
	b.result.Acknowledged += resp.AckMany.Acknowledged
	b.result.AlreadyAcknowledged += resp.AckMany.AlreadyAcknowledged
	b.result.Skipped += resp.AckMany.Skipped
	if resp.AckMany.More {
		b.sel.AfterSeq = resp.AckMany.NextAfterSeq
		return m.ackManyPage()
	}
	b.running = false
	m.status = fmt.Sprintf("acknowledged %d (%d already, %d not yet received)", b.result.Acknowledged, b.result.AlreadyAcknowledged, b.result.Skipped)
	return nil
}

func (m *Model) bulkFailed(reason string) tea.Cmd {
	if m.bulk != nil {
		m.bulk.running = false
	}
	m.status = reason
	return nil
}

// bulkDisconnected ends a running bulk acknowledgment with the connection:
// its pages are idempotent, so /ack all again continues under a fresh bound.
func (m *Model) bulkDisconnected() {
	if m.bulk != nil && m.bulk.running {
		m.bulk.running = false
		m.status = "bulk acknowledgment interrupted by the disconnect; /ack all continues it"
	}
}

// sendRefused tells the operator which bound refused an agent's message,
// since only the operator can free it, and refreshes the usage.
func (m *Model) sendRefused(ev *protocol.SessionEvent) tea.Cmd {
	m.status = safe(ev.AgentID) + "'s message was refused: " + safe(ev.Detail)
	return m.refreshUsage(true)
}

// refreshUsage reads the inbox usage for the header's warning. Inbox changes
// mark the usage dirty; one read is in flight at a time and a change that
// lands meanwhile is read again afterwards, so the warning never goes stale.
// Unforced, a quiet inbox (nothing pending, nothing dirty) costs no request;
// a refusal forces a read, since a global or retained bound can refuse while
// the operator inbox itself is empty.
func (m *Model) refreshUsage(force bool) tea.Cmd {
	if !m.ready() || m.requested(kindInboxStatus, statusForHeader) {
		return nil
	}
	if !force && !m.usageDirty {
		return nil
	}
	m.usageDirty = false
	return m.request(kindInboxStatus, protocol.Message{Type: protocol.TypeOperatorInboxStatus}, statusForHeader)
}

// usageChanged notes an inbox change for the next usage read.
func (m *Model) usageChanged() tea.Cmd {
	m.usageDirty = m.inbox.pending > 0 || m.usage != nil
	return m.refreshUsage(false)
}

// ackReminder is the header's call to action while anything needs
// acknowledgment, and the warning when a sender is near its bound.
func (m *Model) ackReminder() string {
	if m.inbox.pending == 0 {
		return ""
	}
	text := bandStyle.Render(fmt.Sprintf("%d need ack · /ack all", m.inbox.pending))
	if m.usage == nil {
		return text
	}
	for _, u := range m.usage.Senders {
		if u.Unacknowledged*4 >= m.usage.Limits.SenderMessages*3 || u.Bytes*4 >= m.usage.Limits.SenderBytes*3 {
			state := "near bound"
			if u.Unacknowledged >= m.usage.Limits.SenderMessages || u.Bytes >= m.usage.Limits.SenderBytes {
				state = "AT BOUND"
			}
			text += bandStyle.Render(" · ") + bandWarn.Render(fmt.Sprintf("%s %d/%d %s", safe(u.AgentID), u.Unacknowledged, m.usage.Limits.SenderMessages, state))
		}
	}
	return text
}

func (m *Model) bulkView() *BulkAckView {
	if m.bulk == nil {
		return nil
	}
	return &BulkAckView{Running: m.bulk.running, Acknowledged: m.bulk.result.Acknowledged, AlreadyAcknowledged: m.bulk.result.AlreadyAcknowledged, Skipped: m.bulk.result.Skipped}
}
