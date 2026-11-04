package chat

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/pairtask/pairtask/internal/protocol"
)

// Review cards (#305): a review request, here or of a paired workspace's
// agent, as one card from the command to its end. A paired request's card
// is drawn where the command ran and in that workspace's ⇄ tab, one card
// in two places. Where it stands is read by its ask ID when the broker
// says something changed; a read that fails says so, and a request is no
// longer kept only when a read that worked leaves it out.

const (
	cardRemoteReview cardKind = "review-remote"
	cardLocalReview  cardKind = "review-local"
	kindCardAsks              = "card asks"
)

// reviewProgress is what a review card knows.
type reviewProgress struct {
	target    protocol.ReviewTarget
	agent     string
	workspace pairedName
	policy    string
	looking   bool                      // its change is being looked up
	cancelled bool                      // the lookup was left: nothing sent
	pins      *protocol.Revisions       // what it was sent pinned to
	askID     string                    // its request's ID, once the broker queued it
	reviewID  string                    // a review here's ID, once the broker took it
	item      *protocol.ReviewAskedItem // the request as last read
	readSeq   uint64                    // the newest read applied to it
	seen      *protocol.ReviewView      // a review here as last seen
	gone      bool                      // a read that worked no longer has it
	unknown   bool                      // the last read failed
	readDue   bool                      // something changed before it was queued
	settled   bool                      // a review here nothing more will change
}

// isReview: a review card.
func (c *actionCard) isReview() bool { return c.kind == cardRemoteReview || c.kind == cardLocalReview }

// startRemoteCard keeps the card of a request for a paired workspace's
// agent, before its change is looked up.
func (m *Model) startRemoteCard(ask *remoteAsk) {
	c := &actionCard{id: newCardID(), kind: cardRemoteReview, conv: m.conv.selected, contact: string(ask.key), at: m.opts.Now(),
		review: &reviewProgress{target: ask.target, agent: ask.who.agent, workspace: ask.who.workspace, policy: ask.policyWords()}}
	if m.startCard(c) {
		ask.card = c.id
	}
}

// reviewCardOf is the review card with this ID, if kept.
func (m *Model) reviewCardOf(id string) *reviewProgress {
	if c := m.card(id); c != nil && c.review != nil {
		return c.review
	}
	return nil
}

// cardFails ends a card with why its action failed.
func (m *Model) cardFails(id, why string) {
	if c := m.card(id); c != nil {
		c.failure = why
	}
}

// remoteQueued binds a card to its request, now queued, and reads it at
// once if something changed meanwhile.
func (m *Model) remoteQueued(id, askID string) tea.Cmd {
	c := m.card(id)
	if c == nil {
		return nil
	}
	c.id, c.review.askID = askID, askID
	if c.review.readDue {
		c.review.readDue = false
		return m.readCardAsks()
	}
	return nil
}

// cardAsksChanged: something changed that cards may show. Cards not yet
// queued remember it; the others are read.
func (m *Model) cardAsksChanged() tea.Cmd {
	for _, c := range m.cards.list {
		if c.activeRemote() && c.review.askID == "" {
			c.review.readDue = true
		}
	}
	return m.readCardAsks()
}

// activeRemote: a paired request's card that can still change.
func (c *actionCard) activeRemote() bool { return c.kind == cardRemoteReview && !finishedCard(c) }

// readCardAsks reads where the active paired requests stand, eight at a
// time, under the current read generation; each read is numbered, so an
// older one answering late can't undo a newer one.
func (m *Model) readCardAsks() tea.Cmd {
	if !m.session.HasCapability(protocol.CapabilityReviewAskRefs) {
		return nil
	}
	var refs []protocol.ReviewAskRef
	for _, c := range m.cards.list {
		if c.activeRemote() && c.review.askID != "" {
			refs = append(refs, protocol.ReviewAskRef{Contact: c.contact, AskID: c.review.askID})
		}
	}
	var cmds []tea.Cmd
	m.cards.readSeq++
	gen, seq := strconv.FormatUint(m.cards.readGen, 10), strconv.FormatUint(m.cards.readSeq, 10)
	for len(refs) > 0 {
		batch := refs[:min(len(refs), protocol.ReviewAskRefsMax)]
		refs = refs[len(batch):]
		msg := protocol.Message{Type: protocol.TypeOperatorReview, ReviewOp: protocol.ReviewOpAsked, ReviewAskRefs: batch}
		ids := append([]string{gen, seq}, refKeys(batch)...)
		cmds = append(cmds, m.request(kindCardAsks, msg, ids...))
	}
	return tea.Batch(cmds...)
}

func refKeys(refs []protocol.ReviewAskRef) []string {
	keys := make([]string, len(refs))
	for i, r := range refs {
		keys[i] = r.Contact + "\x00" + r.AskID
	}
	return keys
}

// cardAsksResult takes one batch's answer: a read from an older generation
// is dropped; a failed one leaves the batch's cards as they were, unknown;
// one that worked updates them, and those it leaves out are gone.
func (m *Model) cardAsksResult(req pendingRequest, resp protocol.Message) tea.Cmd {
	if req.ids[0] != strconv.FormatUint(m.cards.readGen, 10) {
		return nil
	}
	found := map[string]protocol.ReviewAskedItem{}
	ok := resp.Type == protocol.TypeOperatorReviewResult && resp.ReviewAsked != nil
	if ok {
		for _, it := range resp.ReviewAsked.Items {
			found[it.Contact+"\x00"+it.AskID] = it
		}
	}
	seq, _ := strconv.ParseUint(req.ids[1], 10, 64)
	for _, key := range req.ids[2:] {
		contact, askID, _ := strings.Cut(key, "\x00")
		it, has := found[key]
		m.cardReadAnswer(protocol.ReviewAskRef{Contact: contact, AskID: askID}, readAnswer{it, seq, has, ok})
	}
	return nil
}

// readAnswer is what a batch's answer says of one request: ok, the read
// worked; has, the request was in it, as it; seq, which read it was.
type readAnswer struct {
	it      protocol.ReviewAskedItem
	seq     uint64
	has, ok bool
}

func (m *Model) cardReadAnswer(ref protocol.ReviewAskRef, a readAnswer) {
	for _, c := range m.cards.list {
		if !c.asks(ref) || a.seq <= c.review.readSeq {
			continue // another card, or a newer read already applied
		}
		r := c.review
		r.readSeq = a.seq
		switch {
		case !a.ok:
			r.unknown = true
		case a.has:
			it := a.it
			r.item, r.unknown, r.gone = &it, false, false // the newest read decides
		default:
			r.gone, r.unknown = true, false
		}
	}
}

// asks: the paired request's card for this request.
func (c *actionCard) asks(ref protocol.ReviewAskRef) bool {
	return c.kind == cardRemoteReview && c.contact == ref.Contact && c.review.askID == ref.AskID
}

// bumpCardReads fences reads in flight and, with active cards left, reads
// them again.
func (m *Model) bumpCardReads() tea.Cmd {
	m.cards.readGen++
	return m.readCardAsks()
}

// reviewFinished: a review card nothing more will change.
func reviewFinished(c *actionCard) bool {
	r := c.review
	switch {
	case c.failure != "" || r.cancelled || r.gone:
		return true
	case c.kind == cardRemoteReview:
		return r.item != nil && !askedOpen[r.item.State]
	}
	return r.settled
}

// reviewFailed: it ended badly, red.
func reviewFailed(c *actionCard) bool {
	if c.failure != "" {
		return true
	}
	return c.readDot() == dotRed
}

// readDot is the dot of a paired request as last read, or of a review
// here as last seen, else none.
func (c *actionCard) readDot() reviewDot {
	r := c.review
	switch {
	case c.kind == cardRemoteReview && r.item != nil:
		return askDot(r.item.State)
	case c.kind == cardLocalReview && r.seen != nil:
		return reviewHereDot(*r.seen)
	}
	return dotNone
}

// reviewTitle and reviewFooter frame a review card.
func (m *Model) reviewTitle(c *actionCard) string {
	return "Review request · " + targetName(c.review.target)
}

func (m *Model) reviewFooter(c *actionCard) string {
	if c.kind == cardLocalReview {
		return safe(c.review.agent) + " here · /reviews"
	}
	return safe(c.review.agent) + "@" + safe(string(c.review.workspace)) + " · /reviews"
}

// reviewSteps are what is known of a review request, in order.
func (m *Model) reviewSteps(c *actionCard) []string {
	r := c.review
	green := func(s string) string { return dotGreen.mark(lipgloss.NewStyle(), m.look(m.reviewSig.dotOff)) + " " + s }
	red := func(s string) string { return dotRed.mark(lipgloss.NewStyle(), m.look(false)) + " " + s }
	switch {
	case c.failure != "":
		return []string{red(safe(c.failure))}
	case r.cancelled:
		return []string{mutedStyle.Render("Lookup cancelled: nothing sent")}
	case r.looking:
		return []string{green("Looking up " + targetName(r.target) + "…")}
	case c.kind == cardLocalReview:
		return m.localReviewSteps(c, green)
	case r.askID == "":
		return []string{green("Sending…")}
	}
	return append([]string{"✓ Queued here · pinned " + pinsShort(*r.pins) + " · " + r.policy}, m.remoteStateSteps(c)...)
}

func pinsShort(p protocol.Revisions) string {
	short := func(s string) string { return s[:min(len(s), 7)] }
	return short(p.Head) + " (base " + short(p.Base) + ")"
}

// remoteStateSteps are where a paired request stands, as last read.
func (m *Model) remoteStateSteps(c *actionCard) []string {
	r := c.review
	switch {
	case !m.session.HasCapability(protocol.CapabilityReviewAskRefs):
		return []string{mutedStyle.Render("status: see /reviews")}
	case r.gone:
		return []string{mutedStyle.Render("no longer kept here")}
	case r.item == nil && r.unknown:
		return []string{mutedStyle.Render("status unknown (read failed)")}
	case r.item == nil:
		return nil
	}
	steps := append([]string{m.theirStep(c)}, refusalSteps(c)...)
	if r.unknown {
		steps = append(steps, mutedStyle.Render("status unknown (read failed)"))
	}
	return steps
}

// theirStep is where their side has a paired request, as last read.
func (m *Model) theirStep(c *actionCard) string {
	it, d := c.review.item, c.readDot()
	ws := workspaceLabel(safe(m.contactTitle(contactKey(c.contact))))
	line := d.mark(lipgloss.NewStyle(), m.look(d.blinks() && m.reviewSig.dotOff)) + " " +
		theirState(it.State, it.Reviewer, ws) + verdictWords(*it)
	return strings.TrimSpace(line)
}

// refusalSteps say why their side refused a request, and for not_shared
// what fixes it.
func refusalSteps(c *actionCard) []string {
	it := c.review.item
	if it.Reason == "" || c.readDot() != dotRed {
		return nil
	}
	steps := []string{"Refused: " + safe(it.Reason)}
	if it.Reason == "not_shared" {
		steps = append(steps, "Their operator can allow it with /pair reviews "+safe(c.review.agent)+" ask; then send a new request.")
	}
	return steps
}

func verdictWords(it protocol.ReviewAskedItem) string {
	out := ""
	if it.Verdict != "" {
		out += " · " + safe(it.Verdict)
	}
	if it.URL != "" {
		out += " · " + safe(it.URL)
	}
	return out
}

// startLocalCard keeps the card of a review request here.
func (m *Model) startLocalCard(id string, spec protocol.ReviewSpec) {
	m.startCard(&actionCard{id: id, kind: cardLocalReview, conv: m.conv.selected, at: m.opts.Now(),
		review: &reviewProgress{target: spec.ReviewTarget, agent: spec.Reviewer}})
}

// localBound binds a local card to the review the broker made of it.
func (m *Model) localBound(id string, r *protocol.ReviewView) {
	if c := m.card(id); c != nil && r != nil {
		c.id, c.review.reviewID = r.ID, r.ID
	}
}

// localReviewSteps are where a review here stands, from the reviews read.
func (m *Model) localReviewSteps(c *actionCard, green func(string) string) []string {
	r := c.review
	if r.reviewID == "" {
		return []string{green("Requesting…")}
	}
	if r.seen == nil {
		return []string{"✓ Requested"}
	}
	v, d := *r.seen, c.readDot()
	line := d.mark(lipgloss.NewStyle(), m.look(d.blinks() && m.reviewSig.dotOff)) + " " + safe(v.State)
	if v.Verdict != nil {
		line += " · " + safe(v.Verdict.Outcome)
	}
	line += " · publication " + safe(v.Publication.State)
	if rc := v.Publication.Receipt; rc != nil && rc.URL != "" {
		line += " · " + safe(rc.URL)
	}
	return []string{"✓ Requested", strings.TrimSpace(line)}
}

// reviewByID is a review here as last read.
func (m *Model) reviewByID(id string) (protocol.ReviewView, bool) {
	for _, r := range m.reviews.list {
		if r.ID == id {
			return r, true
		}
	}
	if d := m.reviews.detail; d != nil && d.ID == id {
		return *d, true
	}
	return protocol.ReviewView{}, false
}

// syncLocalCards keeps each review here as last seen, so a later page
// without it erases nothing, and marks those nothing more will change.
func (m *Model) syncLocalCards() {
	for _, c := range m.cards.list {
		if c.kind != cardLocalReview || c.review.reviewID == "" {
			continue
		}
		if v, ok := m.reviewByID(c.review.reviewID); ok {
			pending := v.Publication.State == protocol.PublicationPending || v.Publication.State == protocol.PublicationSubmitting
			c.review.seen, c.review.settled = &v, !openReview(v.State) && !pending
		}
	}
}
