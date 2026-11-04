package chat

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Bounds on action cards (#305): a session keeps at most cardWindow, and a
// conversation draws at most cardsDrawn, the active first. What isn't
// drawn is counted honestly, by what is known of it, with where to look;
// a failure is never dropped silently, and /cards clear is the only way a
// finished card or a count goes.

const cardsDrawn = 8

// drop removes the card at i, counting a failure its conversation must
// still be told of.
func (cs *actionCards) drop(i int) {
	if c := cs.list[i]; failedCard(c) {
		if cs.failed == nil {
			cs.failed = map[viewKey]int{}
		}
		cs.failed[c.conv]++
	}
	cs.list = slices.Delete(cs.list, i, i+1)
}

// countUntracked counts an action that runs without a card.
func (cs *actionCards) countUntracked(c *actionCard) {
	if cs.untracked == nil {
		cs.untracked = map[viewKey]map[cardKind]int{}
	}
	if cs.untracked[c.conv] == nil {
		cs.untracked[c.conv] = map[cardKind]int{}
	}
	cs.untracked[c.conv][c.kind]++
}

// inConversation are a conversation's cards, the active first, then the
// newest.
func (cs *actionCards) inConversation(conv viewKey) []*actionCard {
	var out []*actionCard
	for i := len(cs.list) - 1; i >= 0; i-- {
		if c := cs.list[i]; c.shownIn(conv) {
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(a, b *actionCard) int { return boolOrder(finishedCard(a), finishedCard(b)) })
	return out
}

func boolOrder(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// CardOverflowView is what a conversation can't draw as cards: active
// cards past the bound, failures past it or past the window, and actions
// run without a card.
type CardOverflowView struct{ InProgress, Failures, Untracked int }

// overflow is a conversation's.
func (cs *actionCards) overflow(conv viewKey) CardOverflowView {
	v := CardOverflowView{Failures: cs.failed[conv]}
	for i, c := range cs.inConversation(conv) {
		switch {
		case i < cardsDrawn:
		case !finishedCard(c):
			v.InProgress++
		case failedCard(c):
			v.Failures++
		}
	}
	for _, n := range cs.untracked[conv] {
		v.Untracked += n
	}
	return v
}

// overflowLines say what isn't drawn, and where to look.
func (cs *actionCards) overflowLines(conv viewKey) []string {
	v := cs.overflow(conv)
	var lines []string
	if v.InProgress > 0 {
		lines = append(lines, fmt.Sprintf("%d more cards in progress · /reviews · /pair status", v.InProgress))
	}
	if v.Failures > 0 {
		lines = append(lines, fmt.Sprintf("%d earlier failures · /reviews · /pair status · /cards clear", v.Failures))
	}
	if v.Untracked > 0 {
		lines = append(lines, cs.untrackedLine(conv))
	}
	return lines
}

// untrackedLine counts the uncarded actions by kind, claiming nothing of
// where they stand.
func (cs *actionCards) untrackedLine(conv viewKey) string {
	var parts []string
	words := map[cardKind]string{cardShare: "share changes", cardUnshare: "unshare changes", cardRemoteReview: "review requests to paired workspaces",
		cardLocalReview: "review requests here"}
	for _, k := range []cardKind{cardLocalReview, cardRemoteReview, cardShare, cardUnshare} {
		if n := cs.untracked[conv][k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, words[k]))
		}
	}
	return strings.Join(parts, " and ") + " not shown as cards · /reviews · /pair status"
}

// clear hides a conversation's finished cards there, and its counts; a
// card is dropped once hidden everywhere it is drawn. Active cards stay.
func (cs *actionCards) clear(conv viewKey) int {
	n := 0
	for _, c := range cs.list {
		if c.shownIn(conv) && finishedCard(c) {
			c.hide(conv)
			n++
		}
	}
	cs.list = slices.DeleteFunc(cs.list, (*actionCard).clearedEverywhere)
	delete(cs.failed, conv)
	delete(cs.untracked, conv)
	return n
}

// drawnIn: the card is drawn in conv, where it was done or, for a request
// of a paired workspace's agent, in that workspace's tab.
func (c *actionCard) drawnIn(conv viewKey) bool {
	return c.conv == conv || c.kind == cardRemoteReview && contactKey(c.contact) == conv
}

// shownIn: drawn in conv and not cleared there.
func (c *actionCard) shownIn(conv viewKey) bool { return c.drawnIn(conv) && !c.cleared[conv] }

// hide clears the card from conv.
func (c *actionCard) hide(conv viewKey) {
	if c.cleared == nil {
		c.cleared = map[viewKey]bool{}
	}
	c.cleared[conv] = true
}

// clearedEverywhere: hidden wherever it is drawn.
func (c *actionCard) clearedEverywhere() bool {
	if !c.cleared[c.conv] {
		return false
	}
	return c.kind != cardRemoteReview || c.cleared[contactKey(c.contact)]
}

// CardView is one card for tests and the CLI.
type CardView struct {
	ID, Kind, Conversation string
	Steps                  []string
	Active, Finished       bool
}

// cardViews are every card kept, oldest first.
func (m *Model) cardViews() []CardView {
	out := make([]CardView, 0, len(m.cards.list))
	for _, c := range m.cards.list {
		out = append(out, CardView{ID: c.id, Kind: string(c.kind), Conversation: viewID(c.conv), Steps: m.cardSteps(c),
			Active: !finishedCard(c), Finished: finishedCard(c)})
	}
	return out
}

// cardsCommand is /cards clear.
func (m *Model) cardsCommand(argument) tea.Cmd {
	n := m.cards.clear(m.conv.selected)
	m.status = fmt.Sprintf("cleared %d finished cards here; active ones stay", n)
	return m.bumpCardReads()
}

func cardsArg(a argument) error {
	if strings.TrimSpace(string(a)) != "clear" {
		return errors.New("usage: /cards clear")
	}
	return nil
}
