package chat

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Request kinds, recorded per request ID.
const (
	kindSnapshot = "snapshot"
	kindReceive  = "receive"
	kindReceived = "received"
	kindAck      = "ack"
	kindCatchup  = "catchup"
	kindOlder    = "older"
	kindStages   = "stages"
	kindSend     = "send"
)

// receiptRetryDelay spaces retries of failed receipt confirmations.
const receiptRetryDelay = 2 * time.Second

type inboxState struct {
	pending    int
	headSeq    uint64
	draining   bool
	drainAgain bool
	resumeAt   *protocol.PageRequest // where a scan paused at capacity continues
	queuedAcks map[string]*queuedAck
	quiet      map[string]bool // acknowledgments a reply made: no status line on success
}

// queuedAck is an explicit acknowledgment waiting for confirmed receipt. It
// is bound to the message ID and the adopted body, never to a selection.
type queuedAck struct {
	hash      string
	suspended string
}

func (s *inboxState) disconnected() {
	s.draining, s.drainAgain, s.resumeAt = false, false, nil
	// An automatic acknowledgment on the wire is lost with the connection; only
	// a still-queued one keeps its quiet provenance.
	for id := range s.quiet {
		if s.queuedAcks[id] == nil {
			delete(s.quiet, id)
		}
	}
}

// suspend holds one queued acknowledgment, keeping the first reason.
func (s *inboxState) suspend(id, reason string) {
	if q := s.queuedAcks[id]; q != nil && q.suspended == "" {
		q.suspended = reason
	}
}

func (s *inboxState) suspendAll(reason string) {
	for _, q := range s.queuedAcks {
		q.suspended = reason
	}
}

// startDrain reads the pending inbox under a fixed upper bound until more is
// false. Arrivals during a scan schedule another scan afterwards.
func (m *Model) startDrain() tea.Cmd {
	if m.inbox.draining || m.timeline.paused {
		m.inbox.drainAgain = true
		return nil
	}
	m.inbox.draining = true
	return m.receivePage(0, 0)
}

func (m *Model) receivePage(after, upper uint64) tea.Cmd {
	page := &protocol.PageRequest{AfterSeq: after, UpperSeq: upper, Limit: protocol.OperatorPageMax}
	return m.request(kindReceive, protocol.Message{Type: protocol.TypeOperatorReceive, PageRequest: page})
}

// receivedPage adopts a validated page, then confirms receipt of what it adopted.
func (m *Model) receivedPage(req protocol.PageRequest, page *protocol.OperatorPage, epoch string) tea.Cmd {
	if err := validateReceivePage(req, page); err != nil {
		m.inbox.draining = false
		m.status = err.Error()
		return nil
	}
	adopted, complete := m.adopt(page.Messages, epoch)
	cmds := []tea.Cmd{m.confirmReceipts(adopted)}
	switch {
	case !complete:
		m.pauseDrain(req, page, len(adopted))
	case page.More:
		cmds = append(cmds, m.receivePage(page.NextAfterSeq, page.UpperSeq))
	default:
		cmds = append(cmds, m.finishDrain())
	}
	return tea.Batch(cmds...)
}

// pauseDrain stops at the first message that didn't fit and remembers where
// the scan continues, under the same upper bound.
func (m *Model) pauseDrain(req protocol.PageRequest, page *protocol.OperatorPage, adopted int) {
	after := req.AfterSeq
	if adopted > 0 {
		after = page.Messages[adopted-1].Seq
	}
	m.inbox.draining = false
	m.inbox.resumeAt = &protocol.PageRequest{AfterSeq: after, UpperSeq: page.UpperSeq}
	m.status = "capacity reached: receiving paused until older messages can be released"
}

func (m *Model) finishDrain() tea.Cmd {
	m.inbox.draining = false
	if !m.inbox.drainAgain {
		return nil
	}
	m.inbox.drainAgain = false
	return m.startDrain()
}

// adopt inserts complete bodies, deduplicated by ID. A message already known
// from history still counts as adopted and is confirmed. It stops at the
// first message that can't fit, without skipping it.
func (m *Model) adopt(messages []protocol.Message, epoch string) ([]string, bool) {
	var ids []string
	for _, msg := range messages {
		fresh := newEntry(msg, "received", false)
		if !m.room(fresh) {
			m.timeline.paused = true
			return ids, false
		}
		e, inserted := m.upsert(fresh, epoch)
		m.arrived(e, inserted)
		if e.receipt != ReceiptConfirmed {
			e.receipt = ReceiptPending
		}
		if e.operatorStage == "" {
			e.operatorStage = protocol.StageAccepted
		}
		ids = append(ids, msg.MessageID)
	}
	m.markShown()
	return ids, true
}

// room makes space for a new entry, or for an omitted body becoming
// complete; the promoted entry itself is never evicted to make that space.
func (m *Model) room(fresh *entry) bool {
	known := m.timeline.entries[fresh.msg.MessageID]
	if known == nil {
		return m.timeline.fits(fresh.size, m.opts.Bounds, m.pinned)
	}
	if !known.omitted || fresh.omitted {
		return true
	}
	keep := func(e *entry) bool { return e == known || m.pinned(e) }
	return m.timeline.grow(0, fresh.size-known.size, m.opts.Bounds, keep)
}

// upsert inserts or merges an entry; conflicting content suspends any
// queued acknowledgment for that ID at once.
// upsert admits an entry read in source, the epoch of the page it came on.
// Only a read from the current epoch lets commands target the entry; a late
// page from an older epoch never blesses or downgrades it.
func (m *Model) upsert(fresh *entry, source string) (*entry, bool) {
	e, inserted := m.timeline.upsert(fresh)
	if source == m.epoch {
		e.epoch = source
	}
	if e.conflict {
		m.inbox.suspend(e.msg.MessageID, "content changed")
	}
	return e, inserted
}

// arrived marks a new message unread; rendering it while the selected
// conversation follows the bottom clears the flag (markShown).
func (m *Model) arrived(e *entry, inserted bool) {
	if inserted {
		e.unread = true
		m.reopenFor(e) // new traffic shows a closed conversation again, unselected
	}
}

// pinned entries are never evicted: unconfirmed admissions, queued
// acknowledgments and the unresolved send.
func (m *Model) pinned(e *entry) bool {
	if e.receipt == ReceiptPending || e.receipt == ReceiptFailed {
		return true
	}
	if _, queued := m.inbox.queuedAcks[e.msg.MessageID]; queued {
		return true
	}
	return m.send.intent != nil && m.send.intent.ID == e.msg.MessageID
}

// confirmReceipts sends operator_received in bounded batches for adopted IDs.
func (m *Model) confirmReceipts(ids []string) tea.Cmd {
	var cmds []tea.Cmd
	for len(ids) > 0 {
		n := min(len(ids), m.opts.Bounds.ReceiptBatch)
		batch := ids[:n]
		ids = ids[n:]
		cmds = append(cmds, m.request(kindReceived, protocol.Message{Type: protocol.TypeOperatorReceived, MessageIDs: batch}, batch...))
	}
	return tea.Batch(cmds...)
}

func (m *Model) receiptsConfirmed(req pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeAck || !receiptsCover(resp, req.ids) {
		return m.receiptsFailed(req.ids, "receipt confirmation failed: "+brokerReason(resp))
	}
	var cmds []tea.Cmd
	for _, r := range resp.Receipts {
		if e := m.timeline.entries[r.MessageID]; e != nil {
			e.receipt = ReceiptConfirmed
			e.operatorStage = r.Stage
			cmds = append(cmds, m.sendQueuedAck(e))
		}
	}
	return tea.Batch(append(cmds, m.unpause())...)
}

// receiptsFailed marks the batch failed and retries the same IDs later.
func (m *Model) receiptsFailed(ids []string, reason string) tea.Cmd {
	for _, id := range ids {
		if e := m.timeline.entries[id]; e != nil && e.receipt != ReceiptConfirmed {
			e.receipt = ReceiptFailed
		}
	}
	m.status = reason
	gen := m.gen
	return tea.Tick(receiptRetryDelay, func(time.Time) tea.Msg { return receiptRetryMsg{Gen: gen} })
}

func (m *Model) retryReceipts(msg receiptRetryMsg) tea.Cmd {
	if msg.Gen != m.gen || m.transport == nil {
		return nil
	}
	var ids []string
	for _, e := range m.timeline.order {
		if e.receipt == ReceiptFailed {
			ids = append(ids, e.msg.MessageID)
		}
	}
	return m.confirmReceipts(ids)
}

// unpause resumes receiving once confirmed history can be released again:
// a paused scan continues where it stopped, otherwise a new scan starts.
// Each resume follows a confirmed receipt, so a still-full timeline pauses
// again without spinning.
func (m *Model) unpause() tea.Cmd {
	if !m.timeline.paused {
		return nil
	}
	m.timeline.paused = false
	m.status = ""
	at := m.inbox.resumeAt
	if at == nil || m.inbox.draining {
		return m.startDrain()
	}
	m.inbox.resumeAt, m.inbox.draining = nil, true
	return m.receivePage(at.AfterSeq, at.UpperSeq)
}

func receiptsCover(resp protocol.Message, ids []string) bool {
	if resp.Stage != protocol.StageAdapterReceived {
		return false
	}
	stages := map[string]string{}
	for _, r := range resp.Receipts {
		stages[r.MessageID] = r.Stage
	}
	for _, id := range ids {
		if s := stages[id]; s != protocol.StageAdapterReceived && s != protocol.StageAgentAcknowledged {
			return false
		}
	}
	return true
}

// acknowledge sends the operator's explicit receipt, or queues it until the
// receipt is confirmed; pressing again cancels a queued acknowledgment.
func (m *Model) acknowledge(e *entry) tea.Cmd {
	id := e.msg.MessageID
	switch {
	case e.direction != "received":
		m.status = "only received messages can be acknowledged"
	case e.operatorStage == protocol.StageAgentAcknowledged:
		m.status = "already acknowledged"
	case m.inbox.queuedAcks[id] != nil:
		delete(m.inbox.queuedAcks, id)
		delete(m.inbox.quiet, id)
		m.status = "queued acknowledgment cancelled"
	case m.requested(kindAck, id):
		m.status = "acknowledgment already sent, waiting for the broker"
	case e.receipt == ReceiptConfirmed:
		delete(m.inbox.quiet, id) // explicit: the confirmation is reported
		return m.request(kindAck, protocol.Message{Type: protocol.TypeOperatorAck, MessageID: id}, id)
	default:
		delete(m.inbox.quiet, id)
		m.inbox.queuedAcks[id] = &queuedAck{hash: e.hash}
		m.status = "acknowledgment queued until receipt is confirmed (/ack again cancels)"
	}
	return nil
}

// sendQueuedAck sends a queued acknowledgment only for the same content and
// only while it isn't suspended.
func (m *Model) sendQueuedAck(e *entry) tea.Cmd {
	q := m.inbox.queuedAcks[e.msg.MessageID]
	if q == nil || q.suspended != "" {
		return nil
	}
	if q.hash != e.hash || e.conflict {
		q.suspended = "content changed"
		return nil
	}
	delete(m.inbox.queuedAcks, e.msg.MessageID)
	return m.request(kindAck, protocol.Message{Type: protocol.TypeOperatorAck, MessageID: e.msg.MessageID}, e.msg.MessageID)
}

// acknowledged applies the broker's answer; the quiet provenance of an
// automatic acknowledgment ends with its request, whatever the outcome.
func (m *Model) acknowledged(req pendingRequest, resp protocol.Message) tea.Cmd {
	id := req.ids[0]
	quiet := m.inbox.quiet[id]
	delete(m.inbox.quiet, id)
	if !ackConfirms(resp, id) {
		m.status = "acknowledgment not confirmed: " + brokerReason(resp)
		return nil
	}
	if e := m.timeline.entries[id]; e != nil {
		e.operatorStage = protocol.StageAgentAcknowledged
	}
	if !quiet {
		m.status = "acknowledged by operator (received, not approval)"
	}
	return nil
}

func ackConfirms(resp protocol.Message, id string) bool {
	if resp.Type != protocol.TypeAck {
		return false
	}
	return resp.MessageID == id && resp.Stage == protocol.StageAgentAcknowledged
}
