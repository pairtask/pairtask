package chat

import (
	"fmt"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Pending reviews stand out (#286): the footer says how many review
// requests wait for the operator's decision, in yellow until each is
// decided, with a dot that blinks until /reviews has shown the current set;
// it also counts reviews running here and requests asked of paired
// workspaces. The counts are the broker's: the snapshot's at a baseline,
// read again on each change, never guessed and never zero for unknown.

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
	seen     string // the awaiting set /reviews last showed
	dotOff   bool
	ticking  bool
}

// ReviewSignalView is the footer signal, for tests and the CLI.
type ReviewSignalView struct {
	Known, Stale                    bool
	AwaitingYou, Running, AskedOpen int
	Blinking, DotShown              bool
}

func (m *Model) reviewSignalView() ReviewSignalView {
	s := &m.reviewSig
	v := ReviewSignalView{Known: s.counts != nil, Stale: s.stale, Blinking: m.reviewBlinking()}
	if s.counts != nil {
		v.AwaitingYou, v.Running, v.AskedOpen = s.counts.AwaitingYou, s.counts.Running, s.counts.AskedOpen
	}
	v.DotShown = v.AwaitingYou > 0 && !(v.Blinking && s.dotOff)
	return v
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
	} else {
		s.stale = s.counts != nil
	}
	again := s.again
	s.again = false
	if again {
		return tea.Batch(m.refreshReviewCounts(), m.reviewBlinkTicker())
	}
	return m.reviewBlinkTicker()
}

// reviewBlinking: requests await the operator that /reviews hasn't shown.
func (m *Model) reviewBlinking() bool {
	c := m.reviewSig.counts
	return c != nil && c.AwaitingYou > 0 && c.AwaitingToken != m.reviewSig.seen
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

// onReviewBlink turns the dot over while it blinks; once shown, it stays.
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

// reviewsShown: /reviews showed the current set; the dot stops blinking
// until the set changes.
func (m *Model) reviewsShown() {
	if c := m.reviewSig.counts; c != nil {
		m.reviewSig.seen, m.reviewSig.dotOff = c.AwaitingToken, false
	}
}

// reviewReminder is the footer's part, "" when nothing is pending.
func (m *Model) reviewReminder() string {
	v := m.reviewSignalView()
	if !v.Known || v.AwaitingYou+v.Running+v.AskedOpen == 0 {
		return ""
	}
	text := ""
	if v.AwaitingYou > 0 {
		dot := "  "
		if v.DotShown {
			dot = "● "
		}
		text = bandWarn.Render(fmt.Sprintf("%s%d need you · /reviews", dot, v.AwaitingYou))
	}
	return text + bandStyle.Render(reviewTail(v))
}

// reviewTail is the muted counts after the reviews needing the operator.
func reviewTail(v ReviewSignalView) string {
	tail := ""
	if v.Running > 0 {
		tail += fmt.Sprintf(" · %d running", v.Running)
	}
	if v.AskedOpen > 0 {
		tail += fmt.Sprintf(" · %d asked", v.AskedOpen)
	}
	if v.Stale {
		tail += " · counts stale"
	}
	return tail
}

// reviewsMoved: a review or a contact's messages changed. The counts are
// read again, and an open /reviews panel its first pages.
func (m *Model) reviewsMoved() tea.Cmd {
	return tea.Batch(m.refreshReviewCounts(), m.refreshPendingSections())
}
