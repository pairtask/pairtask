package chat

import (
	"fmt"
	"sort"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// historyState separates the live catch-up scan from the frozen cursor used
// to scroll into older history.
type historyState struct {
	catchup      bool
	catchupAgain bool
	pages        int
	count        int
	scrollUpper  uint64
	loadingOlder bool
	olderDone    bool
	run          *olderRun // a filtered conversation's PgUp paging through history
}

func (s *historyState) disconnected() {
	s.catchup, s.catchupAgain, s.loadingOlder, s.run = false, false, false, nil
}

// startCatchup reads newest first until it overlaps known messages, bounded
// by pages and entries; hitting the bound leaves an explicit gap marker.
func (m *Model) startCatchup() tea.Cmd {
	if m.history.catchup {
		m.history.catchupAgain = true
		return nil
	}
	m.history.catchup, m.history.pages, m.history.count = true, 0, 0
	return m.historyPage(kindCatchup, 0, 0)
}

func (m *Model) historyPage(kind string, before, upper uint64) tea.Cmd {
	page := &protocol.PageRequest{BeforeSeq: before, UpperSeq: upper, Limit: protocol.HistoryMax}
	return m.request(kind, protocol.Message{Type: protocol.TypeOperatorHistory, PageRequest: page})
}

func (m *Model) catchupPage(req protocol.PageRequest, page *protocol.HistoryPage) tea.Cmd {
	if err := validateHistory(req, page); err != nil {
		m.history.catchup = false
		m.status = err.Error()
		return nil
	}
	first := len(m.timeline.order) == 0
	overlap := m.insertHistory(page, true)
	m.history.pages++
	m.history.count += len(page.Entries)
	m.startScroll(page)
	if catchupDone(first, overlap, page) {
		return m.finishCatchup()
	}
	if m.catchupCapped() {
		m.markGap(page.NextBeforeSeq, page.UpperSeq)
		return m.finishCatchup()
	}
	return m.historyPage(kindCatchup, page.NextBeforeSeq, page.UpperSeq)
}

// catchupDone: the first page sets the baseline, and overlap or the end of
// history closes the scan.
func catchupDone(first, overlap bool, page *protocol.HistoryPage) bool {
	if first || overlap {
		return true
	}
	return !page.More
}

func (m *Model) catchupCapped() bool {
	return m.history.pages >= m.opts.Bounds.CatchupPages || m.history.count >= m.opts.Bounds.CatchupEntries
}

// startScroll freezes the bound for scrolling into older history once, on the
// first history page ever read. The inbox drain usually adopts messages before
// that page lands, so this can't depend on the timeline being empty.
func (m *Model) startScroll(page *protocol.HistoryPage) {
	if m.history.scrollUpper == 0 {
		m.history.scrollUpper = page.UpperSeq
		m.history.olderDone = !page.More
	}
}

func (m *Model) finishCatchup() tea.Cmd {
	m.history.catchup = false
	if !m.history.catchupAgain {
		return nil
	}
	m.history.catchupAgain = false
	return m.startCatchup()
}

// markGap flags the oldest caught-up entry: older messages may be missing
// between it and what was already shown.
func (m *Model) markGap(oldest, upper uint64) {
	i := sort.Search(len(m.timeline.order), func(i int) bool { return m.timeline.order[i].msg.Seq >= oldest })
	if i < len(m.timeline.order) {
		m.timeline.order[i].gapBelow, m.timeline.order[i].gapUpper = true, upper
	}
}

// insertHistory adds entries and stages; it reports overlap with known
// messages. History reads never confirm receipt. Arrivals found by catch-up
// count as unread while the selection is scrolled up.
func (m *Model) insertHistory(page *protocol.HistoryPage, arrivals bool) bool {
	overlap := false
	for _, h := range page.Entries {
		_, known := m.timeline.entries[h.Message.MessageID]
		overlap = overlap || known
		fresh := newEntry(h.Message, h.Direction, h.BodyOmitted)
		if !m.room(fresh) {
			m.timeline.paused = true
			break
		}
		e, inserted := m.upsert(fresh, page.Watermark.Epoch)
		if arrivals {
			m.arrived(e, inserted)
		}
		m.applyHistoryStages(e, h, page.Watermark)
	}
	if arrivals {
		m.markShown()
	}
	return overlap
}

// applyHistoryStages takes stages only from a page read in the current
// epoch; a late page from before a broker restart keeps its bodies only.
func (m *Model) applyHistoryStages(e *entry, h protocol.HistoryEntry, wm protocol.Watermark) {
	if wm.Epoch != m.epoch {
		return
	}
	if h.Direction == "received" {
		if e.operatorStage != protocol.StageAgentAcknowledged {
			e.operatorStage = h.OperatorStage
		}
		return
	}
	for _, st := range h.Recipients {
		e.applyStage(st, wm)
	}
	e.stagesTruncated = h.ReceiptsTruncated
}

// loadOlder fills the gap below gap, or loads history below the oldest
// loaded message when gap is nil.
func (m *Model) loadOlder(gap *entry) tea.Cmd {
	if m.history.loadingOlder {
		return nil
	}
	if m.history.scrollUpper == 0 {
		m.status = "history not loaded yet"
		return nil
	}
	before, upper := m.timeline.oldestSeq(), m.history.scrollUpper
	if gap != nil && gap.gapBelow {
		before, upper = gap.msg.Seq, max(upper, gap.gapUpper)
	} else if m.history.olderDone {
		m.status = "no older messages"
		return nil
	}
	m.history.loadingOlder = true
	return m.historyPage(kindOlder, before, upper)
}

func (m *Model) olderPage(req protocol.PageRequest, page *protocol.HistoryPage) tea.Cmd {
	m.history.loadingOlder = false
	if err := validateHistory(req, page); err != nil {
		m.status = err.Error()
		m.history.run = nil
		return nil
	}
	gapEntry := m.timeline.entries[seqID(m.timeline, req.BeforeSeq)]
	before := len(m.timeline.order)
	overlap := m.insertHistory(page, false)
	filled := overlap || !page.More
	if gapEntry != nil && filled {
		gapEntry.gapBelow = false
	}
	if req.BeforeSeq <= m.timeline.oldestSeq() || !page.More {
		m.history.olderDone = !page.More
	}
	return m.continueOlder(page, before)
}

// olderRun is one PgUp from the top of a filtered conversation: it keeps
// paging global history through pages with nothing for that conversation,
// bounded by the catch-up page bound, and reports where it ended.
type olderRun struct {
	key   viewKey
	pages int
}

// continueOlder requests the next page while a filtered run found nothing
// yet, and reports the end state otherwise.
func (m *Model) continueOlder(page *protocol.HistoryPage, _ int) tea.Cmd {
	run := m.history.run
	if run == nil {
		return nil
	}
	run.pages++
	matched := m.pageMatches(run.key, page)
	exhausted := m.history.olderDone || !page.More
	if matched || exhausted {
		m.endOlderRun(matched, exhausted)
		return nil
	}
	if run.pages >= m.opts.Bounds.CatchupPages {
		m.history.run = nil
		m.status = fmt.Sprintf("no older messages from %s in the last %d pages; PgUp continues", run.key.label(), run.pages)
		return nil
	}
	m.status = fmt.Sprintf("loading older history (%d pages)", run.pages)
	return m.loadOlder(nil)
}

// pageMatches reports whether the page brought anything for the view.
func (m *Model) pageMatches(key viewKey, page *protocol.HistoryPage) bool {
	for _, e := range m.timeline.order {
		if inPage(e, page) && m.inView(e, key) {
			return true
		}
	}
	return false
}

// inPage reports an entry within the sequence window a page covered.
func inPage(e *entry, page *protocol.HistoryPage) bool {
	return e.msg.Seq < page.UpperSeq && e.msg.Seq >= page.NextBeforeSeq
}

// endOlderRun closes a run that found something or ran out of history.
func (m *Model) endOlderRun(matched, exhausted bool) {
	m.history.run = nil
	if !matched && exhausted {
		m.status = "no older messages"
	}
}

func seqID(t timeline, seq uint64) string {
	for _, e := range t.order {
		if e.msg.Seq == seq {
			return e.msg.MessageID
		}
	}
	return ""
}

// loadStages pages the selected sent message's recipient stages.
func (m *Model) loadStages(e *entry) tea.Cmd {
	if e.direction != "sent" {
		m.status = "stages exist only for sent messages"
		return nil
	}
	if m.requested(kindStages, e.msg.MessageID) {
		return nil
	}
	return m.request(kindStages, protocol.Message{Type: protocol.TypeOperatorMessageStages, MessageID: e.msg.MessageID, AfterAgent: lastAgent(e), Limit: protocol.StagesMax}, e.msg.MessageID)
}

func lastAgent(e *entry) string {
	if !e.stagesTruncated {
		return ""
	}
	last := ""
	for id := range e.stages {
		last = max(last, id)
	}
	return last
}

func (m *Model) stagesPage(req pendingRequest, resp protocol.Message) tea.Cmd {
	if !validStagesPage(req.ids[0], resp) {
		m.status = "stages not loaded: " + brokerReason(resp)
		return nil
	}
	page := resp.Stages
	e := m.timeline.sent(page.MessageID)
	if e == nil || page.Watermark.Epoch != m.epoch {
		return nil
	}
	for _, st := range page.Recipients {
		e.applyStage(st, page.Watermark)
	}
	e.stagesTruncated = page.More
	return nil
}

func validStagesPage(id string, resp protocol.Message) bool {
	if resp.Type != protocol.TypeOperatorMessageStages || resp.Stages == nil {
		return false
	}
	return resp.Stages.MessageID == id && validStages(resp.Stages.Recipients)
}
