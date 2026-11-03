package chat

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Pending reviews stand out (#286, #298): the footer says how many review
// requests wait for the operator's decision, how many reviews run here and
// how many requests to paired workspaces are open, led by a dot that blinks
// while any of that is under way, and how many failed, in red until
// /reviews has shown every one. The counts are the broker's: the
// snapshot's at a baseline, read again on each change, never guessed and
// never zero for unknown.

const kindReviewCounts = "review counts"

// reviewBlinkEvery is the dot's half period.
const reviewBlinkEvery = 600 * time.Millisecond

// ReviewBlinkMsg advances the dot's blink; chat schedules it only while
// the dot blinks.
type ReviewBlinkMsg struct{}

// reviewSignal is the counts as last read, and the dot.
type reviewSignal struct {
	counts   *protocol.ReviewCounts // nil until read, or from a broker without them
	stale    bool                   // the last read failed: these counts may be old
	inFlight bool
	again    bool   // a change arrived while a read was in flight
	gen      uint64 // the baseline's generation: an older read's answer is dropped
	failSeen string // the failure set /reviews last showed in full
	dotOff   bool
	ticking  bool
}

// ReviewSignalView is the footer signal, for tests and the CLI. Process is
// "yellow" while requests wait for the operator, "green" while reviews or
// requests are under way, "" for neither; RedShown while failures /reviews
// hasn't shown in full exist.
type ReviewSignalView struct {
	Known, Stale                    bool
	AwaitingYou, Running, AskedOpen int
	Blinking, DotShown              bool
	Process                         string
	Failed                          int
	RedShown                        bool
}

func (m *Model) reviewSignalView() ReviewSignalView {
	s := &m.reviewSig
	v := ReviewSignalView{Known: s.counts != nil, Stale: s.stale}
	if s.counts != nil {
		v.AwaitingYou, v.Running, v.AskedOpen = s.counts.AwaitingYou, s.counts.Running, s.counts.AskedOpen
		v.Failed, v.RedShown = m.reviewFailures()
	}
	if s.counts != nil && !s.stale {
		v.Process = m.reviewProcess() // stale counts claim nothing by colour
	} else {
		v.RedShown = false
	}
	v.Blinking = v.Process != ""
	v.DotShown = v.Blinking && !s.dotOff
	return v
}

// failuresServed: the broker counts and pages failures (#298).
func (m *Model) failuresServed() bool {
	return m.session.HasCapability(protocol.CapabilityReviewFailures)
}

// activeAsks are this side's requests under way: the broker's count of
// open ones not failed, or, from an older broker, every open one.
func (m *Model) activeAsks() int {
	c := m.reviewSig.counts
	if m.failuresServed() && c.AskedActive != nil {
		return *c.AskedActive
	}
	return c.AskedOpen
}

// reviewProcess is what is under way: requests waiting for the operator
// first, then reviews or requests in progress.
func (m *Model) reviewProcess() string {
	c := m.reviewSig.counts
	switch {
	case c.AwaitingYou > 0:
		return "yellow"
	case c.Running+m.activeAsks() > 0:
		return "green"
	}
	return ""
}

// reviewFailures are how many failed, and whether red shows: some failed
// that /reviews hasn't shown in full.
func (m *Model) reviewFailures() (int, bool) {
	c := m.reviewSig.counts
	if !m.failuresServed() {
		return 0, false
	}
	return c.Failed, c.Failed > 0 && c.FailedToken != m.reviewSig.failSeen
}

// setReviewCounts installs a baseline's counts: none from an older broker.
// A read still in flight belongs to the old baseline and is dropped.
func (m *Model) setReviewCounts(snap *protocol.OperatorSnapshot) tea.Cmd {
	s := &m.reviewSig
	s.gen++
	s.inFlight, s.again, s.stale = false, false, false
	s.counts = nil
	if snap.ReviewCounts != nil {
		c := *snap.ReviewCounts
		s.counts = &c
	}
	return m.reviewBlinkTicker()
}

// refreshReviewCounts reads the counts again: one read at a time, and one
// more after it when a change arrived meanwhile.
func (m *Model) refreshReviewCounts() tea.Cmd {
	s := &m.reviewSig
	if s.counts == nil {
		return nil // no baseline counts: a broker without them, or none read yet
	}
	if s.inFlight {
		s.again = true
		return nil
	}
	s.inFlight = true
	msg := protocol.Message{Type: protocol.TypeOperatorReview, ReviewOp: protocol.ReviewOpCounts}
	return m.request(kindReviewCounts, msg, strconv.FormatUint(s.gen, 10))
}

// reviewCountsResult keeps the counts read; a failure keeps the last ones,
// stale, never zero.
func (m *Model) reviewCountsResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	s := &m.reviewSig
	if req.ids[0] != strconv.FormatUint(s.gen, 10) {
		return nil // read before the baseline that replaced it
	}
	s.inFlight = false
	if resp.Type == protocol.TypeOperatorReviewResult && resp.ReviewCounts != nil {
		c := *resp.ReviewCounts
		s.counts, s.stale = &c, false
		m.markFailuresShown() // the shown set may be the one now counted
	} else {
		s.stale = s.counts != nil
	}
	again := s.again
	s.again = false
	if again {
		return tea.Batch(m.refreshReviewCounts(), m.reviewBlinkTicker(), m.syncFailedSection())
	}
	return tea.Batch(m.reviewBlinkTicker(), m.syncFailedSection())
}

// reviewBlinking: something is under way, as counts read now say; it
// blinks for as long as it lasts, whether or not /reviews showed it.
func (m *Model) reviewBlinking() bool {
	return m.reviewSig.counts != nil && !m.reviewSig.stale && m.reviewProcess() != ""
}

// reviewBlinkTicker schedules the next blink while the dot blinks.
func (m *Model) reviewBlinkTicker() tea.Cmd {
	s := &m.reviewSig
	if s.ticking || !m.reviewBlinking() {
		return nil
	}
	s.ticking = true
	return tea.Tick(reviewBlinkEvery, func(time.Time) tea.Msg { return ReviewBlinkMsg{} })
}

// onReviewBlink turns the dot over while something is under way; once
// nothing is, it resets, lit for whatever comes next.
func (m *Model) onReviewBlink() tea.Cmd {
	s := &m.reviewSig
	s.ticking = false
	if !m.reviewBlinking() {
		s.dotOff = false
		return nil
	}
	s.dotOff = !s.dotOff
	return m.reviewBlinkTicker()
}

// failuresShown: /reviews showed every failure of the set with this
// token; red stays off while the counts name that set.
func (m *Model) failuresShown(token string) {
	m.reviewSig.failSeen = token
}

// reviewReminder is the footer's part, "" when nothing is pending.
func (m *Model) reviewReminder() string {
	v := m.reviewSignalView()
	quiet := v.Process == "" && !v.RedShown && v.AwaitingYou+v.Running+v.AskedOpen == 0
	switch {
	case !v.Known:
		return ""
	case quiet && v.Stale:
		return bandStyle.Render("review counts stale · /reviews") // a failed read never looks like nothing pending
	case quiet:
		return ""
	}
	parts := m.footerParts(v)
	if v.Stale {
		parts = append(parts, bandStyle.Render("counts stale"))
	}
	return strings.Join(parts, bandStyle.Render(" · "))
}

// reviewsMoved: a review or a contact's messages changed. The counts are
// read again, and an open /reviews panel its first pages.
func (m *Model) reviewsMoved() tea.Cmd {
	return tea.Batch(m.refreshReviewCounts(), m.refreshPendingSections())
}
