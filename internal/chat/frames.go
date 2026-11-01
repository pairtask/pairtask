package chat

import (
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

var errTooLarge = errors.New("message too large: operator messages are limited to 512 KiB")

// frame handles one frame and keeps exactly one listen command pending.
func (m *Model) frame(msg FrameMsg) tea.Cmd {
	if msg.Gen != m.gen || m.transport == nil {
		return nil
	}
	next := listen(m.gen, m.transport)
	if msg.Frame.Type == protocol.TypeEvent {
		return tea.Batch(next, m.event(msg.Frame.Event))
	}
	return tea.Batch(next, m.response(msg.Frame))
}

// event applies notifications in revision order; a gap, a new epoch or
// resync_required means a new snapshot.
func (m *Model) event(ev *protocol.SessionEvent) tea.Cmd {
	if ev == nil {
		return m.malformed("event without a body")
	}
	if !m.baseline || m.seen(ev.Watermark) {
		return nil
	}
	if m.needsResync(ev) {
		if ev.Watermark.Epoch != m.epoch {
			m.newEpoch(ev.Watermark.Epoch)
		}
		return m.resync()
	}
	m.revision = ev.Watermark.Revision
	return m.applyEvent(ev)
}

// seen reports an event already covered by the current baseline.
func (m *Model) seen(wm protocol.Watermark) bool {
	return wm.Epoch == m.epoch && wm.Revision <= m.revision
}

// needsResync reports resync_required, a new epoch or a revision gap.
func (m *Model) needsResync(ev *protocol.SessionEvent) bool {
	if ev.Kind == protocol.EventResyncRequired {
		return true
	}
	return ev.Watermark.Epoch != m.epoch || ev.Watermark.Revision != m.revision+1
}

func (m *Model) applyEvent(ev *protocol.SessionEvent) tea.Cmd {
	switch ev.Kind {
	case protocol.EventConversationChanged:
		return m.conversationChanged(ev)
	case protocol.EventInboxChanged:
		return m.inboxChanged(ev)
	case protocol.EventStageChanged:
		m.stageChanged(ev)
	case protocol.EventPresenceChanged:
		m.agents.apply(ev)
		m.statuses.presence(ev)
	case protocol.EventSendRefused:
		return m.sendRefused(ev)
	case protocol.EventContactMessagesChanged:
		return m.contactsChanged(ev.ContactKey)
	}
	if refresh := refreshEvents[ev.Kind]; refresh != nil {
		return refresh(m)
	}
	return nil
}

// refreshEvents read again what changed; they carry nothing to apply.
var refreshEvents = map[string]func(*Model) tea.Cmd{
	// The snapshot carries statuses, claims and rooms.
	protocol.EventStatusChanged:  (*Model).refreshStatuses,
	protocol.EventClaimsChanged:  (*Model).refreshStatuses,
	protocol.EventRoomsChanged:   (*Model).refreshStatuses,
	protocol.EventPairingChanged: (*Model).pairingChanged,
	protocol.EventReviewsChanged: (*Model).reviewsChanged,
}

func (m *Model) conversationChanged(ev *protocol.SessionEvent) tea.Cmd {
	if ev.Direction == "received" {
		return m.startDrain()
	}
	return m.startCatchup()
}

func (m *Model) inboxChanged(ev *protocol.SessionEvent) tea.Cmd {
	if ev.Pending != nil {
		m.inbox.pending = *ev.Pending
	}
	grew := ev.HeadSeq > m.inbox.headSeq
	m.inbox.headSeq = ev.HeadSeq
	if grew {
		return tea.Batch(m.startDrain(), m.usageChanged())
	}
	return m.usageChanged()
}

func (m *Model) stageChanged(ev *protocol.SessionEvent) {
	if !validStage(ev.Stage) {
		return
	}
	e := m.timeline.sent(ev.MessageID)
	if e == nil {
		return
	}
	e.applyStage(protocol.RecipientStage{AgentID: ev.AgentID, SessionID: ev.SessionID, Stage: ev.Stage}, ev.Watermark)
}

// response routes a reply to its request. Unknown IDs are ignored: they
// belong to an older connection.
func (m *Model) response(resp protocol.Message) tea.Cmd {
	if resp.RequestID == "" {
		return m.malformed(fmt.Sprintf("response without request_id (%q)", resp.Type))
	}
	req, next := m.release(resp.RequestID)
	if req.kind == "" {
		return next
	}
	return tea.Batch(next, m.handle(req, resp))
}

type responseHandler func(*Model, pendingRequest, protocol.Message) tea.Cmd

// responseHandlers route a response by the kind of request it answers.
var responseHandlers = map[string]responseHandler{
	kindSnapshot:       func(m *Model, _ pendingRequest, resp protocol.Message) tea.Cmd { return m.snapshotLoaded(resp) },
	kindReceive:        (*Model).receiveResponse,
	kindReceived:       (*Model).receiptsConfirmed,
	kindAck:            (*Model).acknowledged,
	kindCatchup:        (*Model).catchupResponse,
	kindOlder:          (*Model).olderResponse,
	kindStages:         (*Model).stagesPage,
	kindSend:           (*Model).sendResult,
	kindInboxStatus:    (*Model).inboxStatusResponse,
	kindAckMany:        (*Model).ackManyResponse,
	kindRoom:           (*Model).roomResult,
	kindRoomDelete:     (*Model).roomDeleted,
	kindPairing:        (*Model).pairingResult,
	kindPairingQuiet:   (*Model).quietPairingResult,
	kindReview:         (*Model).reviewResult,
	kindReviewQuiet:    (*Model).quietReviewResult,
	kindContactList:    (*Model).contactListResult,
	kindContactSend:    (*Model).contactSendResult,
	kindContactRead:    (*Model).contactReadResult,
	kindContactShare:   (*Model).contactShareResult,
	kindContactName:    (*Model).contactNameResult,
	kindContactPause:   (*Model).contactPauseResult,
	kindContactAddress: (*Model).contactAddressResult,
	kindContactAsk:     (*Model).contactAskResult,
}

func (m *Model) handle(req pendingRequest, resp protocol.Message) tea.Cmd {
	if h := responseHandlers[req.kind]; h != nil {
		return h(m, req, resp)
	}
	return nil
}

func (m *Model) receiveResponse(req pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeOperatorPage {
		m.inbox.draining = false
		m.status = "inbox not loaded: " + brokerReason(resp)
		return nil
	}
	return m.receivedPage(req.page, resp.Page, req.epoch)
}

func (m *Model) catchupResponse(req pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeOperatorHistory {
		m.history.catchup = false
		m.status = "history not loaded: " + brokerReason(resp)
		return nil
	}
	return m.catchupPage(req.page, resp.History)
}

func (m *Model) olderResponse(req pendingRequest, resp protocol.Message) tea.Cmd {
	if resp.Type != protocol.TypeOperatorHistory {
		m.history.loadingOlder, m.history.run = false, nil
		m.status = "history not loaded: " + brokerReason(resp)
		return nil
	}
	return m.olderPage(req.page, resp.History)
}

// snapshotLoaded installs the baseline: bodies stay, metadata resets, then
// the inbox, history and any unknown send are reconciled.
func (m *Model) snapshotLoaded(resp protocol.Message) tea.Cmd {
	if !validSnapshot(resp) {
		return m.malformed("snapshot: " + brokerReason(resp))
	}
	snap := resp.Snapshot
	if snap.Watermark.Epoch != m.epoch {
		m.newEpoch(snap.Watermark.Epoch)
	}
	m.revision, m.baseline = snap.Watermark.Revision, true
	m.agents = agentView{list: snap.Agents, total: snap.AgentsTotal, partial: snap.AgentsTruncated}
	m.statuses.set(snap)
	m.claims.set(snap)
	m.rooms, m.roomsCut = snap.Rooms, snap.RoomsTruncated
	m.statuses.inFlight, m.statuses.dirty = false, false // a new baseline supersedes any refresh
	m.inbox.pending, m.inbox.headSeq = snap.InboxPending, snap.InboxHeadSeq
	m.timeline.resetStages()
	for _, s := range snap.RecentSent {
		if e := m.timeline.entries[s.MessageID]; e != nil {
			for _, st := range s.Recipients {
				e.applyStage(st, snap.Watermark)
			}
			e.stagesTruncated = s.ReceiptsTruncated
		}
	}
	m.pairing.listed = false // a /pair picker reads again after a reconnection
	return tea.Batch(m.startDrain(), m.startCatchup(), m.resendUnknown(), m.refreshUsage(snap.InboxPending > 0), m.statusTicker(),
		m.refreshContacts(), m.reviewsReconnected(), m.contactsReconnected())
}

func validSnapshot(resp protocol.Message) bool {
	if resp.Type != protocol.TypeOperatorSnapshot || resp.Snapshot == nil {
		return false
	}
	return resp.Snapshot.Watermark.Epoch != ""
}

// malformed reports a response the chat can't accept; it's not retried as if
// the transport had failed.
func (m *Model) malformed(detail string) tea.Cmd {
	m.status = ErrMalformed.Error() + ": " + detail
	return nil
}

func brokerReason(resp protocol.Message) string {
	if resp.Code != "" {
		return fmt.Sprintf("%q: %q", resp.Code, resp.Detail)
	}
	return fmt.Sprintf("unexpected %q response", resp.Type)
}

func isMalformed(err error) bool {
	return errors.Is(err, ErrMalformed)
}

func validStage(stage string) bool {
	switch stage {
	case protocol.StageAccepted, protocol.StageAdapterReceived, protocol.StageAgentAcknowledged:
		return true
	}
	return false
}

func validStages(stages []protocol.RecipientStage) bool {
	for _, st := range stages {
		if !validStage(st.Stage) {
			return false
		}
	}
	return true
}

// validateReceivePage checks a whole inbox page before anything is adopted.
func validateReceivePage(req protocol.PageRequest, page *protocol.OperatorPage) error {
	if page == nil {
		return fmt.Errorf("%w: missing inbox page", ErrMalformed)
	}
	if !sameUpper(req.UpperSeq, page.UpperSeq) {
		return fmt.Errorf("%w: inbox page upper_seq changed", ErrMalformed)
	}
	last, ok := inboxAscending(req.AfterSeq, page)
	if !ok {
		return fmt.Errorf("%w: inbox page holds an invalid or out-of-order message", ErrMalformed)
	}
	if !cursorConsistent(page.NextAfterSeq, last, len(page.Messages) == 0, page.More) {
		return fmt.Errorf("%w: inbox page cursor is inconsistent", ErrMalformed)
	}
	return nil
}

// sameUpper reports whether a continued scan kept its frozen upper bound.
func sameUpper(requested, got uint64) bool {
	return requested == 0 || got == requested
}

// cursorConsistent checks a page cursor against its last row; an empty page
// can't promise more.
func cursorConsistent(next, last uint64, empty, more bool) bool {
	if empty && more {
		return false
	}
	return next == last
}

// inboxAscending checks every message and their strictly ascending order
// within (after, upper]; it returns the last sequence.
func inboxAscending(after uint64, page *protocol.OperatorPage) (uint64, bool) {
	seen := map[string]bool{}
	last := after
	for _, msg := range page.Messages {
		if !validInboxMessage(msg) || seen[msg.MessageID] {
			return 0, false
		}
		if msg.Seq <= last || msg.Seq > page.UpperSeq {
			return 0, false
		}
		seen[msg.MessageID], last = true, msg.Seq
	}
	return last, true
}

// validInboxMessage requires a complete message that reached the operator
// inbox; the broker sends a payload-less message as JSON null.
func validInboxMessage(msg protocol.Message) bool {
	return msg.Type == protocol.TypeMsg && reachesOperator(msg) && msg.MessageID != "" && len(msg.Payload) > 0
}

// reachesOperator: a message to the operator, or an agent's public "*" (v7),
// whose frozen recipients include the operator inbox.
func reachesOperator(msg protocol.Message) bool {
	return msg.To == protocol.OperatorID || (msg.To == protocol.Broadcast && msg.Origin == protocol.OriginAgent)
}

// validateHistory checks a whole history page before anything is inserted.
func validateHistory(req protocol.PageRequest, page *protocol.HistoryPage) error {
	if page == nil {
		return fmt.Errorf("%w: missing history page", ErrMalformed)
	}
	if !sameUpper(req.UpperSeq, page.UpperSeq) {
		return fmt.Errorf("%w: history upper_seq changed", ErrMalformed)
	}
	last, ok := historyDescending(req.BeforeSeq, page)
	if !ok {
		return fmt.Errorf("%w: history holds an invalid or out-of-order entry", ErrMalformed)
	}
	if !cursorConsistent(page.NextBeforeSeq, last, len(page.Entries) == 0, page.More) {
		return fmt.Errorf("%w: history cursor is inconsistent", ErrMalformed)
	}
	return nil
}

// historyDescending checks every entry and their strictly descending order
// inside the page window; it returns the last sequence, or before if empty.
func historyDescending(before uint64, page *protocol.HistoryPage) (uint64, bool) {
	seen := map[string]bool{}
	last := before
	for i, h := range page.Entries {
		if !validHistoryEntry(h) || seen[h.Message.MessageID] {
			return 0, false
		}
		seq := h.Message.Seq
		if !inWindow(seq, before, page.UpperSeq) || !descends(i, seq, last) {
			return 0, false
		}
		seen[h.Message.MessageID], last = true, seq
	}
	return last, true
}

func descends(i int, seq, last uint64) bool {
	return i == 0 || seq < last
}

func inWindow(seq, before, upper uint64) bool {
	return seq > 0 && seq <= upper && (before == 0 || seq < before)
}

func validHistoryEntry(h protocol.HistoryEntry) bool {
	if !completeHistoryMessage(h) {
		return false
	}
	switch h.Direction {
	case "sent":
		return h.Message.Origin == protocol.OriginOperator && validStages(h.Recipients)
	case "received":
		return reachesOperator(h.Message) && validStage(h.OperatorStage)
	}
	return false
}

// completeHistoryMessage requires a payload exactly when the body isn't omitted.
func completeHistoryMessage(h protocol.HistoryEntry) bool {
	if h.Message.Type != protocol.TypeMsg || h.Message.MessageID == "" {
		return false
	}
	return h.BodyOmitted == (len(h.Message.Payload) == 0)
}
