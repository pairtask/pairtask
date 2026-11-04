package chat

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Review advisories (#304) on a paired request's card: what the broker knew
// when it queued it, and what a check of explicit pins found after it was
// sent. Advice only: nothing here holds, changes or resends a request.

// pinCheckBudget bounds a check of pins.
const pinCheckBudget = 5 * time.Second

// pinCheckedMsg is a check's answer for a card, under the generation it
// started in.
type pinCheckedMsg struct {
	card   *actionCard
	gen    uint64
	advice []protocol.ReviewAdvisory
}

// checkPinsAfterSending checks the request's pins alongside sending it;
// without a checker the card says it wasn't.
func (m *Model) checkPinsAfterSending(ask remoteAsk, pins protocol.Revisions) tea.Cmd {
	c := m.card(ask.card)
	if c == nil || c.review == nil || c.failure != "" {
		return nil
	}
	check := m.opts.Bounds.CheckPins
	if check == nil {
		c.review.pinSteps = []string{mutedStyle.Render("Pins not checked: unsupported")}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), pinCheckBudget)
	m.cards.pinCancels = append(m.cards.pinCancels, cancel)
	gen, target := m.cards.pinGen, ask.target
	return func() tea.Msg {
		defer cancel()
		return pinCheckedMsg{card: c, gen: gen, advice: check(ctx, target, pins)}
	}
}

// cancelPinChecks leaves the checks in flight behind: their answers come
// to nothing.
func (m *Model) cancelPinChecks() {
	m.cards.pinGen++
	for _, cancel := range m.cards.pinCancels {
		cancel()
	}
	m.cards.pinCancels = nil
}

// pinsChecked puts a check's answer on its card, if it is still asked for.
func (m *Model) pinsChecked(msg pinCheckedMsg) tea.Cmd {
	c := msg.card
	if msg.gen != m.cards.pinGen || !slicesContains(m.cards.list, c) || c.review.pins == nil {
		return nil
	}
	c.review.pinSteps = pinCheckSteps(msg.advice, *c.review.pins)
	return nil
}

func slicesContains(list []*actionCard, c *actionCard) bool {
	for _, x := range list {
		if x == c {
			return true
		}
	}
	return false
}

// pinCheckSteps say what the check found, in words.
func pinCheckSteps(advice []protocol.ReviewAdvisory, pins protocol.Revisions) []string {
	short := func(s string) string { return safe(s[:min(len(s), 7)]) }
	var found []string
	for _, a := range advice {
		switch a.Code {
		case protocol.AdvisoryNotChecked:
			return []string{mutedStyle.Render("Pins not checked: " + safe(a.Class))}
		case protocol.AdvisoryHeadMoved:
			found = append(found, "HEAD "+short(pins.Head)+" isn't the change's head (now "+short(a.Current)+")")
		case protocol.AdvisoryHeadMissing:
			found = append(found, "HEAD "+short(pins.Head)+" not found")
		case protocol.AdvisoryBaseMissing:
			found = append(found, "BASE "+short(pins.Base)+" not found")
		}
	}
	if len(found) == 0 {
		return []string{"✓ Pins checked after sending: ok"}
	}
	return []string{errStyle.Render("Pins checked after sending: " + strings.Join(found, "; "))}
}

// keepAdvice keeps the broker's advice on the card, from a broker that
// gives it.
func (m *Model) keepAdvice(id string, advice []protocol.ReviewAdvisory) {
	if r := m.reviewCardOf(id); r != nil && m.session.HasCapability(protocol.CapabilityReviewAdvisories) {
		r.advice = advice
	}
}

// adviceSteps are the broker's advice, in words.
func (m *Model) adviceSteps(c *actionCard) []string {
	ws := safe(m.contactTitle(contactKey(c.contact)))
	var steps []string
	for _, a := range c.review.advice {
		switch a.Code {
		case protocol.AdvisoryNotInDirectory:
			steps = append(steps, needStyle.Render("Note: "+safe(c.review.agent)+" isn't in what "+ws+" shares"+asOf(a.At)))
		case protocol.AdvisoryRefusedNotShared:
			steps = append(steps, needStyle.Render("Note: "+ws+" refused the last request to "+safe(c.review.agent)+" (not_shared)"+atTime(a.At)))
		}
	}
	return steps
}

func asOf(at *time.Time) string {
	if at == nil {
		return ""
	}
	return " (as of " + at.Local().Format("15:04") + ")"
}

func atTime(at *time.Time) string {
	if at == nil {
		return ""
	}
	return " at " + at.Local().Format("15:04")
}

// reviewerNote is what this chat already knows, before sending, of the
// paired agent a /review line names: that its workspace's sharing, as last
// read, leaves it out (#304). Nothing is read for it, and nothing is held.
func (m *Model) reviewerNote(command, rest string) string {
	if command != "review" {
		return ""
	}
	for _, word := range strings.Fields(rest) {
		who, ok := reviewerWord(word).paired()
		if !ok {
			continue
		}
		key, problem := m.pairedWorkspace(who.workspace)
		t := m.contacts.threads[string(key)]
		if problem != "" || t == nil || !t.dirKnown || slicesHas(t.directory, who.agent) {
			return ""
		}
		return safe(who.agent) + " isn't in what " + safe(string(who.workspace)) + " shares, as last read"
	}
	return ""
}

func slicesHas(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
