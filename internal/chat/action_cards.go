package chat

import (
	"context"
	"slices"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/google/uuid"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Action cards (#305): what the operator just did, kept in sight where it
// was done and updated in place as evidence arrives, apart from the status
// line that the next event overwrites. A card claims a step only on its
// evidence: saved here when the broker answered, their side has it only
// when the contact answered exactly the set this action left.

// cardWindow bounds the actions a session keeps cards for.
const cardWindow = 32

type cardKind string

const (
	cardShare   cardKind = "share"
	cardUnshare cardKind = "unshare"
)

// actionCard is one action and what is known of it.
type actionCard struct {
	id       string
	kind     cardKind
	conv     viewKey // where it was done, and is drawn
	contact  string
	agents   []string
	at       time.Time
	saved    bool
	failure  string
	digest   string           // the set this action left, once saved
	ackAt    *time.Time       // when their side answered that set
	already  bool             // it had that set before this action
	replaced *time.Time       // a later change left another set: sticky
	review   *reviewProgress  // a review request's, else nil
	cleared  map[viewKey]bool // /cards clear hid it there
}

func newCardID() string { return "card-" + uuid.NewString() }

// actionCards are the session's cards, oldest first, and per conversation
// what can't be drawn as one.
type actionCards struct {
	list       []*actionCard
	failed     map[viewKey]int               // failures dropped past the window, until cleared
	untracked  map[viewKey]map[cardKind]int  // actions run with every card still active
	readGen    uint64                        // fences reads of where requests stand
	readSeq    uint64                        // numbers them, newest last
	pinGen     uint64                        // fences checks of pins (#304)
	pinCancels map[uint64]context.CancelFunc // the checks in flight, by number
	pinSeq     uint64                        // numbers them
}

// startCard keeps a new action's card. Past the window the oldest finished
// card makes room, a failure counted on; with every card active the action
// runs uncarded and is only counted.
func (m *Model) startCard(c *actionCard) bool {
	cards := &m.cards
	if len(cards.list) >= cardWindow {
		i := slices.IndexFunc(cards.list, finishedCard)
		if i < 0 {
			cards.countUntracked(c)
			return false
		}
		cards.drop(i)
	}
	cards.list = append(cards.list, c)
	return true
}

// finishedCard: nothing more will change on it.
func finishedCard(c *actionCard) bool {
	if c.review != nil {
		return reviewFinished(c)
	}
	return c.failure != "" || c.replaced != nil || c.ackAt != nil
}

// failedCard: it ended badly.
func failedCard(c *actionCard) bool {
	if c.review != nil {
		return reviewFailed(c)
	}
	return c.failure != ""
}

// card is the action with this ID, if kept.
func (m *Model) card(id string) *actionCard {
	for _, c := range m.cards.list {
		if c.id == id {
			return c
		}
	}
	return nil
}

// shareSaved records the broker's answer to a share or unshare: the set it
// left, then what the page says of their side.
func (m *Model) shareSaved(c *actionCard, page *protocol.ContactMessagePage) {
	c.saved, c.digest = true, protocol.DirectoryDigest(page.Shared)
	m.cardsSeePage(c.contact, page)
}

// cardsSeePage moves a contact's share cards on by what a page of it says.
func (m *Model) cardsSeePage(contact string, page *protocol.ContactMessagePage) {
	if !m.session.HasCapability(protocol.CapabilityContactShareSync) {
		return
	}
	for _, c := range m.cards.list {
		if c.awaitsPage(contact) {
			c.see(page, m.opts.Now())
		}
	}
}

// awaitsPage: a saved share card of this contact a page can still move on.
func (c *actionCard) awaitsPage(contact string) bool {
	return c.contact == contact && c.saved && c.replaced == nil
}

// see takes a page's evidence: another set shared now replaces this
// action for good; their answer to this action's set confirms it.
func (c *actionCard) see(page *protocol.ContactMessagePage, now time.Time) {
	if page.SharedDigest != c.digest {
		c.replaced = &now
		return
	}
	if c.ackAt == nil && c.answeredIn(page) {
		at := *page.SharedAckAt
		c.ackAt, c.already = &at, at.Before(c.at)
	}
}

// answeredIn: the page has their answer to this action's set.
func (c *actionCard) answeredIn(page *protocol.ContactMessagePage) bool {
	return page.SharedAckDigest == c.digest && page.SharedAckAt != nil
}

// cardLines are a conversation's cards, at most cardsDrawn, drawn steady
// with the oldest drawn first, then what isn't drawn.
func (m *Model) cardLines(conv viewKey) []string {
	drawn := m.cards.inConversation(conv)
	drawn = drawn[:min(len(drawn), cardsDrawn)]
	slices.SortStableFunc(drawn, func(a, b *actionCard) int { return a.at.Compare(b.at) })
	var lines []string
	for _, c := range drawn {
		lines = append(append(lines, ""), m.drawCard(c)...)
	}
	for _, line := range m.cards.overflowLines(conv) {
		lines = append(lines, mutedStyle.Render(line))
	}
	return lines
}

var (
	cardEdge  = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	cardTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
)

// drawCard is one card: a title, its steps, and what to know, each line
// wrapped to the width.
func (m *Model) drawCard(c *actionCard) []string {
	if c.review != nil {
		return m.frameCard(m.reviewTitle(c), m.reviewSteps(c), m.reviewFooter(c))
	}
	ws := safe(m.contactTitle(contactKey(c.contact)))
	title, footer := "Share agent · "+agentNames(c.agents, "no agents")+" → "+ws, "Message sharing; review permission is separate (/pair reviews)"
	if c.kind == cardUnshare {
		title, footer = "Unshare agent · "+agentNames(c.agents, "no agents")+" ← "+ws, "They can no longer message "+agentNames(c.agents, "them")
	}
	return m.frameCard(title, m.cardSteps(c), footer)
}

// frameCard draws a title, steps and a footer in the card's frame.
func (m *Model) frameCard(title string, steps []string, footer string) []string {
	lines := []string{cardEdge.Render("┌ ") + cardTitle.Render(title)}
	for _, step := range steps {
		lines = append(lines, cardEdge.Render("│ ")+step)
	}
	return append(lines, cardEdge.Render("└ ")+mutedStyle.Render(footer))
}

// cardSteps are what is known of the action, in order.
func (m *Model) cardSteps(c *actionCard) []string {
	if c.review != nil {
		return m.reviewSteps(c)
	}
	switch {
	case c.failure != "":
		return []string{dotRed.mark(lipgloss.NewStyle(), m.look(false)) + " Not saved: " + safe(c.failure)}
	case !c.saved:
		return []string{dotGreen.mark(lipgloss.NewStyle(), m.look(m.reviewSig.dotOff)) + " Saving…"}
	}
	return []string{"✓ Saved here", m.peerStep(c)}
}

// peerStep is what is known of their side.
func (m *Model) peerStep(c *actionCard) string {
	switch {
	case !m.session.HasCapability(protocol.CapabilityContactShareSync):
		return mutedStyle.Render("Peer update unconfirmed (this broker can't tell)")
	case c.replaced != nil:
		return mutedStyle.Render("Superseded by a later change (" + c.replaced.Local().Format("15:04") + ")")
	case c.ackAt != nil && c.already:
		return "✓ Their side already had it (since " + c.ackAt.Local().Format("15:04") + ")"
	case c.ackAt != nil:
		return "✓ Their side has it (" + c.ackAt.Local().Format("15:04") + ")"
	}
	return dotGreen.mark(lipgloss.NewStyle(), m.look(m.reviewSig.dotOff)) + " Peer update pending"
}
